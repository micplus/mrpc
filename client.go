package mrpc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/micplus/mrpc/codec"
)

// net/rpc: 函数能被rpc调用的条件
// the method’s type is exported.
// the method is exported.
// the method has two arguments, both exported (or builtin) types.
// the method’s second argument is a pointer.
// the method has return type error.
// 封装所有信息到函数调用中，附加Done指示异步调用完成
type Call struct {
	// 向服务器发送的请求数据
	Name string
	Args any
	// 由Client动态生成
	Seq uint64

	// 来自服务器的响应数据
	Error error
	Reply any

	// 通知异步调用完成，用来阻塞获取*Call
	Done chan *Call
}

// 传回自己(replyCall := <-argsCall.Done，replyCall与argsCall指向相同)
func (c *Call) done() {
	c.Done <- c
}

// 一个client可以发起多个调用，client入口可以被多个协程获取，
// 注意并发性
type Client struct {
	// 编解码器
	cc codec.Codec
	// 8字节，4字节的Magic，4字节的编码器号
	flag []byte
	// 用来保护发送请求数据流，以免并发请求在同一个连接上混杂在一起
	sending sync.Mutex // protect following
	// 请求消息头部，这个数据可以复用，每次发送时加锁，发送出去后就可以改成别的数据
	header codec.Header

	// 对client状态的修改需要加互斥锁，保护下面的4项
	mu sync.Mutex // protect following
	// 请求序号，修改是互斥的以免重复
	seq uint64
	// 记录当前尚未完成的请求，支持异步调用
	pending map[uint64]*Call
	// 主动关闭标志
	closing bool // user has called Close
	// 崩溃标志
	shutdown bool // server has told us to stop
}

var ErrShutDown = errors.New("connection shut down")

// 关闭客户端，修改closing状态，通过codec关闭连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || c.closing {
		return ErrShutDown
	}
	c.closing = true
	return c.cc.Close()
}

// 检查状态，若客户端关闭或崩溃则不可用
func (c *Client) IsAvaliable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.shutdown && !c.closing
}

// 将新的调用信息置入pending map当中，更新client的序号
func (c *Client) addCall(call *Call) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 客户端已不可用
	if c.closing || c.shutdown {
		return 0, ErrShutDown
	}
	call.Seq = c.seq
	c.pending[call.Seq] = call
	c.seq++
	return call.Seq, nil
}

// 按序号从pending map中移除Call并将其返回
func (c *Client) removeCall(seq uint64) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	call := c.pending[seq] // 指针的零值就是nil
	delete(c.pending, seq) // 幂等，重复无效
	return call
}

// 发生错误时的回调函数，需要终止客户端当前的一切调用。
// 将错误信息写到call当中
func (c *Client) terminateCalls(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 阻止写数据，更新错误信息
	c.sending.Lock()
	defer c.sending.Unlock()

	c.shutdown = true
	// 修改所有的调用信息
	for _, call := range c.pending {
		call.Error = err
		call.done()
	}
}

// 接收服务器传来的响应
func (c *Client) receive() {
	// 客户端需要不断地从到服务器的连接中读取数据，
	// 数据来自于远程，需要经过网络传输。
	// 客户端使用codec从数据流中读响应头、响应体，
	// 读响应头如果读到EOF，说明连接中已经没有新的数据，可以退出。
	// 响应头部负载了错误信息，若有错，舍弃响应体；
	// 从响应头获取响应call的序号seq，取得pending[seq]
	// 若call不存在，说明服务器数据传输出错，或者这个seq已经被移除了
	// 每当读取完一个body，就通知对应的call.done()

	var err error
	for err == nil {
		var h codec.Header
		if err = c.cc.ReadHeader(&h); err != nil { // 读不出数据EOF
			break // return
		}
		// 读到一个响应的头部，标志着它对应的调用已经执行完毕，调用结果写给call
		call := c.removeCall(h.Seq)
		switch {
		case call == nil: // 没能取到c.pending[h.Seq]
			// call已经不存在/header在网络中传输出错，舍弃接下来的body
			err = c.cc.ReadBody(nil)
		case h.Error != "": // 根据header得知服务器返回了一个错误
			call.Error = errors.New(h.Error)
			err = c.cc.ReadBody(nil)
			call.done()
		default: // 正常情况
			if err = c.cc.ReadBody(call.Reply); err != nil {
				call.Error = errors.New("reading body error: " + err.Error())
			}
			call.done()
		}
	}
	// 从字节流中读取时发生了错误，客户端断开连接，终止未完成的调用
	c.terminateCalls(err)
}

// 检查codec支持，接管连接，写Magic(发送握手消息)，初始化Client并在另一goroutine启动
func NewClient(conn net.Conn, codecType uint32) (*Client, error) {
	ncf, ok := codec.NewCodecFuncMap[codecType]
	if !ok {
		err := fmt.Errorf("invalid codec type %v", codecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}

	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf, Magic)
	binary.BigEndian.PutUint32(buf[4:], codecType)
	_, err := conn.Write(buf)
	if err != nil {
		log.Println("rpc client: write conn error:", err)
		// 向连接写入时发生错误，断开连接
		conn.Close()
		return nil, err
	}

	client := &Client{
		cc:      ncf(conn),
		flag:    buf,
		seq:     1, // gopl: 使用零值所具备的含义 => 正确的值从1开始
		pending: make(map[uint64]*Call),
	}

	go client.receive()
	return client, nil
}

// 实现一个包级的Dial方法方便用户操作
func Dial(network, address string, codecType ...uint32) (*Client, error) {
	ccType := codec.GobType
	switch len(codecType) {
	case 0:
	case 1:
		ccType = codecType[0]
	default:
		err := errors.New("use case: Dial(\"tcp\", \"127.0.0.1:1234\", [codecType]")
		log.Println("rpc client:", err)
		return nil, err
	}
	conn, err := net.Dial(network, address)
	if err != nil {
		log.Println("rpc client: dial error:", err)
		return nil, err
	}
	client, err := NewClient(conn, ccType)
	if err != nil {
		// 创建客户端失败，断开连接
		conn.Close()
		log.Println("rpc client: create client error:", err)
		return nil, err
	}
	return client, nil
}

// 将一次调用信息发送给服务器
func (c *Client) send(call *Call) {
	// 保护发送数据头部。在Client中，我们封装了一个codec.Header方便这项工作，但要加锁
	c.sending.Lock()
	defer c.sending.Unlock()

	// 客户端接收到用户指定的服务名、参数、返回值、(通道)，剩下的由客户端进行包装
	seq, err := c.addCall(call)
	if err != nil { // 这个call不能被添加进pending map，取消执行，写报错信息到call
		call.Error = err
		call.done()
		return
	}

	call.Seq = seq
	// header存放有上一个调用的遗留数据，刷新之
	c.header.Seq = seq
	c.header.Name = call.Name
	c.header.Error = ""

	if err := c.cc.Write(&c.header, call.Args); err != nil {
		// 向连接写入时发生错误，废弃这次请求
		if call := c.removeCall(seq); call != nil { // 为空可以直接跳过
			call.Error = err
			call.done()
		}
	}
}

// 异步调用
// arithCall := cli.Go("Arith.Multiply", args, &reply, nil)
// replyCall := <-arithCall.Done
func (c *Client) Go(name string, args, reply any, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 1) // 非阻塞的，可以继续执行下去
	}

	call := &Call{
		Name:  name,
		Args:  args,
		Reply: reply,
		Done:  done,
	}
	c.send(call)

	return call
}

// 同步调用
func (c *Client) Call(name string, args, reply any) error {
	call := <-c.Go(name, args, reply, nil).Done
	return call.Error
}
