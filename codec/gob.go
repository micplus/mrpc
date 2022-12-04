package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

type GobCodec struct {
	conn io.ReadWriteCloser // 编解码器不需要关心连接地址信息，只用读写关闭
	buf  *bufio.Writer      // bufio带缓冲区防阻塞，数据先写缓冲，优化执行效率
	dec  *gob.Decoder       // 从连接中读数据，解码
	enc  *gob.Encoder       // 向缓冲区写数据，编码
}

// 接收连接，返回一个可以从/向连接读写信息的编解码器
func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(buf),
	}
}

// 读Header
func (c *GobCodec) ReadHeader(h *Header) error {
	return c.dec.Decode(h)
}

// 读Body
func (c *GobCodec) ReadBody(body any) error {
	return c.dec.Decode(body)
}

// 先写缓冲，再把缓冲写入连接
func (c *GobCodec) Write(h *Header, body any) (err error) {
	// 把缓冲区数据写进conn
	defer func() {
		c.buf.Flush()
		// 在if语句块中的局部变量err作为返回值被赋值给有名返回值err
		// defer在计算返回值之后、清空上下文之前执行
		// 返回值err在这里被捕捉到，无论是哪个err都能在此作出响应
		if err != nil {
			c.Close()
		}
	}()

	if err := c.enc.Encode(h); err != nil {
		log.Println("rpc codec: gob encoding header error:", err)
		return err
	}
	if err := c.enc.Encode(body); err != nil {
		log.Println("rpc codec: gob encoding body error:", err)
		return err
	}

	return nil
}

func (c *GobCodec) Close() error {
	return c.conn.Close()
}
