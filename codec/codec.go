package codec

import "io"

// codec is short for COder-DECoder

// err := client.Call("Arith.Multiply", args, &reply)
//
// RPC调用需要有：服务名.方法名、参数、响应（返回值+错误信息）
//
// 附加一个请求序号用来分隔不同的请求
//
// 这三个类型是固定的，所以放在一起。
// 客户端只需要包装它真正需要的args/reply类型
type Header struct {
	Seq   uint64
	Name  string
	Error string
}

// Codec原则上应当支持不同的编解码方式，
// 抽象出一个接口，解析gob json等，或用户自己实现一个Codec
// 编解码操作数据流，它要做到读写关闭操作
type Codec interface {
	// 从连接流读数据到Header
	ReadHeader(*Header) error
	// 从连接读数据到body(传pointer)
	ReadBody(any) error
	// 一次性把header和body写到流中，让它们连在一起，但不保证写到conn时的并发安全
	Write(*Header, any) error
	// 关闭连接
	io.Closer // Close() error
}

const (
	GobType uint32 = iota
	JSONType
	CustomType // ...
)

type NewCodecFunc func(io.ReadWriteCloser) Codec

// 由此，服务器可以接受多种编码请求，客户端传来编码类型，服务端可以检查是否接受
var NewCodecFuncMap map[uint32]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[uint32]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec // 注册支持的编码类型
}
