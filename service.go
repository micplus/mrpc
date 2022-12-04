package mrpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

// 通过反射注册服务
// 注册服务：
// type Arith int	// receiver
// func (*Arith) Add(args []int, reply *int) error
// 		receiver name args 		reply		return value
//
// rpc.Register(new(Arith))

// 具体的方法（Add、Multiply等）方法名、参数、返回值
type methodType struct {
	method    reflect.Method
	ArgType   reflect.Type
	ReplyType reflect.Type

	// 辅助记录调用次数
	numCalls uint64
}

func (mt *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&mt.numCalls)
}

// 从reflect.Type创建一个空的具体值，客户端发起RPC时，参数域可以是指针或结构体的值
func (mt *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// 如果是指针类型
	if mt.ArgType.Kind() == reflect.Pointer {
		// reflect.New也是new，Elem()取指向的值
		argv = reflect.New(mt.ArgType.Elem())
	} else {
		argv = reflect.New(mt.ArgType).Elem()
	}
	return argv
}

// 创建空值，传入的返回值域只能是个指针
func (mt *methodType) newReplyv() reflect.Value {
	replyv := reflect.New(mt.ReplyType.Elem())
	switch replyv.Elem().Kind() {
	case reflect.Map: // 根据mt中的ReplyType，创建一个对应的map
		replyv.Elem().Set(reflect.MakeMap(mt.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(mt.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

// type Arith int
// func (*Arith) Add(args []int, reply *int) error
// 被解释成
// func Add(*Arith, []int, *int) error
// 这个Arith类的指针要保留下来
// 注册Arith到服务，要有类型的名称、方法接收者（类型空对象或指针）、接收的导出且符合rpc规定的方法map
type service struct {
	name   string
	typ    reflect.Type // Arith类型 typ=reflect.ValueOf(rcvr)
	rcvr   reflect.Value
	method map[string]*methodType
}

// receiver可以是结构体或指向结构体的指针
// 略去reflect.
// Indirect解引用
// x := ValueOf(rcvr) -> Value.Kind()==Pointer
// x = Indirect(x)	-> Value.Kind()==Struct
func newService(rcvr any) *service {
	s := new(service)
	// 绑定到结构体的方法，可以用指针接收也可以不是指针
	s.rcvr = reflect.ValueOf(rcvr)
	s.typ = reflect.TypeOf(rcvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	if !ast.IsExported(s.name) { // 不是导出的结构体，rpc服务注册失败
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	s.registerMethods()

	return s
}

// 取出传入结构体的所有方法名，及其实体，映射到方法表。
// 函数也是引用类型的值
func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	// Arith结构可以注册多种方法，不一定是供rpc调用的
	for i := 0; i < s.typ.NumMethod(); i++ {
		m := s.typ.Method(i)
		mt := m.Type
		// func(*Arith, int, *int) error
		if mt.NumIn() != 3 || mt.NumOut() != 1 {
			continue
		}
		// 返回值是error类型
		if mt.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		argType, replyType := mt.In(1), mt.In(2)
		if !isExportedOrBuiltin(argType) || !isExportedOrBuiltin(replyType) {
			continue
		}
		s.method[m.Name] = &methodType{
			method:    m,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s", s.name, m.Name)
	}
}

// rpc的参数需要是可访问的导出类型或内置类型
func isExportedOrBuiltin(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

// 使用反射来调用方法
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1) // 记录

	rets := m.method.Func.Call([]reflect.Value{s.rcvr, argv, replyv})
	if iErr := rets[0].Interface(); iErr != nil {
		return iErr.(error)
	}
	return nil
}
