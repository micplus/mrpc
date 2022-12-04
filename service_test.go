package mrpc

import (
	"reflect"
	"testing"
)

type Arith int
type Args struct {
	num1, num2 int
}

func (*Arith) Add(args *Args, reply *int) error {
	*reply = args.num1 + args.num2
	return nil
}
func (*Arith) Multiply(args *Args, reply *int) error {
	*reply = args.num1 * args.num2
	return nil
}

func TestService(t *testing.T) {
	s := newService(new(Arith))
	assert(t, len(s.method) == 2, "wrong methods number, want 2 got %d", len(s.method))
	assert(t, s.method["Add"] != nil, "method Add not exist")
	assert(t, s.method["Multiply"] != nil, "method Multiply not exist")

	addType := s.method["Add"]
	argv, replyv := addType.newArgv(), addType.newReplyv()
	args := &Args{1, 2}
	if argv.Kind() == reflect.Pointer {
		argv.Elem().Set(reflect.ValueOf(*args))
	} else {
		argv.Set(reflect.ValueOf(*args))
	}
	err := s.call(addType, argv, replyv)
	assert(t, err == nil && *replyv.Interface().(*int) == 3 && addType.NumCalls() == 1, "call Arith.Add failed")
}

func assert(t *testing.T, cond bool, format string, v ...any) {
	t.Helper()
	if !cond {
		t.Errorf(format, v...)
	}
}
