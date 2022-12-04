package mrpc

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"mrpc/codec"
	"net"
	"reflect"
	"strings"
	"sync"
)

// 证明服务器收到的请求是rpc请求，不是则丢弃
const Magic uint32 = 0x5a2b71c3

// 一次连接，允许发送多个请求从而避免不断建立连接带来的开销
// 客户端发来的数据格式：
// Magic | Type | Header1 | Body1 | Header2 | Body2 ...

// 参照net/rpc的server

type Server struct {
	serviceMap map[string]*service
}

func NewServer() *Server {
	return &Server{
		serviceMap: make(map[string]*service),
	}
}

var DefaultServer = NewServer()

// 把某个类型(指针)的服务注册给server
func (s *Server) Register(rcvr any) error {
	svc := newService(rcvr)
	if _, dup := s.serviceMap[svc.name]; dup {
		return errors.New("rcp server: duplicated service " + svc.name)
	}
	s.serviceMap[svc.name] = svc
	return nil
}

func Register(rcvr any) error {
	return DefaultServer.Register(rcvr)
}

// name="Service.Method"
func (s *Server) findService(name string) (svc *service, mt *methodType, err error) {
	// 检查名称
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		err = errors.New("rpc server: service name must be like \"Service.Method\"")
		return
	}
	sName, mName := name[:dot], name[dot+1:]
	// 寻找service
	var ok bool
	if svc, ok = s.serviceMap[sName]; !ok {
		err = errors.New("rpc server: cannot find service " + sName)
		return
	}
	// 寻找method
	if mt, ok = svc.method[mName]; !ok {
		err = errors.New("rpc server: cannot find method " + mName + " on service " + sName)
		return
	}
	return
}

// 接管listener的Accept方法，循环等待连接，开启goroutine作处理
func (s *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: listener accept error:", err)
			continue
		}
		go s.ServeConn(conn)
	}
}

// net/rpc 有rpc.xxx()，同理
// 接管listener的Accept方法，循环等待连接，开启goroutine作处理
func Accept(lis net.Listener) {
	DefaultServer.Accept(lis)
}

// 处理建立的连接，检查是不是rpc请求、编码是否支持，包装连接给相应的codec处理
func (s *Server) ServeConn(conn net.Conn) {
	defer func() {
		conn.Close()
	}()
	buf := make([]byte, 8)
	if _, err := io.ReadFull(conn, buf); err != nil {
		log.Println("rpc server: read conn error:", err)
		return
	}
	// 检查是否以Magic开头，即是不是rpc请求
	if num := binary.BigEndian.Uint32(buf[:4]); num != Magic {
		log.Printf("rpc server: invalid magic number: %x", num)
		return
	}
	// 检查编码类型
	codecType := binary.BigEndian.Uint32(buf[4:])
	ncf := codec.NewCodecFuncMap[codecType]
	if ncf == nil {
		log.Printf("rpc server: invalid codec type: %v", codecType)
		return
	}
	s.serveCodec(ncf(conn))
}

var invalidRequest = struct{}{}

// 编解码
func (s *Server) serveCodec(cc codec.Codec) {
	defer cc.Close()
	// 由于一次连接允许发送多个请求，处理请求是并发的。对于并发的请求，处理后要把响应数据写到连接。
	// 既然要并发地写数据，而bufio本身没有线程(协程)安全的处理，
	// 无论哪个请求处理协程要写数据，都应该给codec上的字节流（连接）加锁，
	// 防止不同协程的响应数据交织在一起。
	// A Mutex must not be copied after first use.
	mu := new(sync.Mutex)
	// 所有请求都应该被处理，先者要等后者
	// A WaitGroup must not be copied after first use.
	wg := new(sync.WaitGroup)
	for {
		req, err := s.readRequest(cc)
		if err != nil {
			if req == nil { // EOF也是error
				break
			}
			// 写回错误信息
			req.h.Error = err.Error()
			go s.writeResponse(cc, req.h, invalidRequest, mu)
			continue
		}
		wg.Add(1)
		go s.handleRequest(cc, req, mu, wg)
	}
	wg.Wait()

}

// 整合Header、Body，记录了一次调用所用的完整信息
type request struct {
	h *codec.Header

	// 服务、方法、参数、返回值
	svc          *service
	mType        *methodType
	argv, replyv reflect.Value
}

// 读请求头，读到EOF或其它错误就返回
func (s *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read request header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

// 读请求头部，读请求体
func (s *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := s.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}

	req := &request{h: h}
	req.svc, req.mType, err = s.findService(h.Name)
	if err != nil {
		return nil, err
	}
	// 动态地创建方法所绑定的参数类型
	req.argv = req.mType.newArgv()
	req.replyv = req.mType.newReplyv()

	// 交由codec读数据，绑定到argv
	iargv := req.argv.Interface()
	// ReadBody需要接受一个指针
	if req.argv.Kind() != reflect.Pointer {
		iargv = req.argv.Addr().Interface()
	}
	if err := cc.ReadBody(iargv); err != nil {
		log.Println("rpc server: read request body error:", err)
	}
	return req, nil
}

// 写响应数据的协程，加锁
func (s *Server) writeResponse(cc codec.Codec, h *codec.Header, body any, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

// 处理请求，写回响应
func (s *Server) handleRequest(cc codec.Codec, req *request, mu *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()

	if err := req.svc.call(req.mType, req.argv, req.replyv); err != nil {
		req.h.Error = err.Error()
		s.writeResponse(cc, req.h, invalidRequest, mu)
	}
	s.writeResponse(cc, req.h, req.replyv.Interface(), mu)
}
