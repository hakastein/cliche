package cliche

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"sync"

	"github.com/dop251/goja"
)

// Map is a read-only mapping an app gives a script, its keys in its own order.
type Map interface {
	Keys() []string
	Get(key string) (any, bool)
}

// List is a read-only list an app gives a script.
type List interface {
	Len() int
	Index(i int) any
}

// Object is a mapping with its keys in order: one a script built, or one an app builds with NewObject.
type Object struct {
	keys   []string
	values map[string]any
}

// NewObject pairs keys with values in order; a key given twice keeps its last value at its first place.
func NewObject(keys []string, values []any) *Object {
	if len(keys) != len(values) {
		panic(fmt.Sprintf("cliche: NewObject takes as many values as keys, and it was given %d keys and %d values",
			len(keys), len(values)))
	}
	o := &Object{values: make(map[string]any, len(keys))}
	for i, key := range keys {
		if _, held := o.values[key]; !held {
			o.keys = append(o.keys, key)
		}
		o.values[key] = values[i]
	}
	return o
}

func (o *Object) Keys() []string {
	return slices.Clone(o.keys)
}

func (o *Object) Get(key string) (any, bool) {
	value, held := o.values[key]
	return value, held
}

// Func is a function of the app's API. ctx is the context of Run and carries Place.
type Func func(ctx context.Context, args ...any) (any, error)

// Async is a function of the app's API that runs in a goroutine of its own: the script gets a promise of what it
// returns, carried by the bridge as for a Func. ctx is the context of Run and carries Place; Run cancels it when it
// returns and waits for the function, so the function ends once ctx is done.
type Async func(ctx context.Context, args ...any) (any, error)

// Promise is a promise the app settles from any goroutine: a value of the bridge a script awaits. The zero value is
// pending, and the first Settle counts, as in JavaScript. Run waits for work of an Async, not for a Promise: a Promise
// is settled while the script still waits for an Async, or before the script awaits it.
type Promise struct {
	mu      sync.Mutex
	settled bool
	value   any
	err     error
	waiting []func(value any, err error)
}

// Settle fulfils p with value, or rejects it with err when err is not nil.
func (p *Promise) Settle(value any, err error) {
	p.mu.Lock()
	if p.settled {
		p.mu.Unlock()
		return
	}
	p.settled, p.value, p.err = true, value, err
	waiting := p.waiting
	p.waiting = nil
	p.mu.Unlock()
	for _, settle := range waiting {
		settle(value, err)
	}
}

// then calls settle once p is settled: at once when it is.
func (p *Promise) then(settle func(value any, err error)) {
	p.mu.Lock()
	if !p.settled {
		p.waiting = append(p.waiting, settle)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	settle(p.value, p.err)
}

// Getter is a property computed each time a script reads it.
type Getter func(ctx context.Context) (any, error)

// Undefined is the value a script reads as undefined: an optional argument the call did not give.
type Undefined struct{}

type placeKey struct{}

type place struct {
	file         string
	line, column int
}

// Place is the line of the script that called the Func given ctx; an empty file when it is unknown.
func Place(ctx context.Context) (file string, line, column int) {
	at, _ := ctx.Value(placeKey{}).(place)
	return at.file, at.line, at.column
}

// Above this an integer is no longer exact in a double, and goes to a script as a BigInt.
const exactInDouble = 1 << 53

var (
	bytesType       = reflect.TypeOf([]byte(nil))
	arrayBufferType = reflect.TypeOf(goja.ArrayBuffer{})
)

// toJS turns a value of the bridge into a value of the script; a value that is none is a TypeError.
func (e *engine) toJS(value any) goja.Value {
	switch v := value.(type) {
	case nil:
		return goja.Null()
	case Undefined:
		return goja.Undefined()
	case bool, string, float64:
		return e.vm.ToValue(v)
	case int64:
		if v > exactInDouble || v < -exactInDouble {
			return e.vm.ToValue(big.NewInt(v))
		}
		return e.vm.ToValue(v)
	case *big.Int:
		return e.vm.ToValue(v)
	case []byte:
		return e.buffer.WrapBytes(slices.Clone(v))
	case []any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, e.toJS(item))
		}
		return e.vm.NewArray(items...)
	case *Object:
		return e.objectOf(v)
	case Map:
		object := e.vm.NewDynamicObject(&mapValue{engine: e, held: v, read: map[string]goja.Value{}})
		e.origins[object] = v
		return object
	case List:
		object := e.vm.NewDynamicArray(&listValue{engine: e, held: v, read: map[int]goja.Value{}})
		e.origins[object] = v
		return object
	case Func:
		object := e.vm.ToValue(e.function(v)).(*goja.Object)
		e.origins[object] = v
		return object
	case Async:
		object := e.vm.ToValue(e.async(v)).(*goja.Object)
		e.origins[object] = v
		return object
	case *Promise:
		promise, resolve, reject := e.vm.NewPromise()
		v.then(func(value any, err error) { e.post(func() { e.settle(resolve, reject, value, err) }) })
		object := e.vm.ToValue(promise).(*goja.Object)
		e.origins[object] = v
		return object
	case Getter:
		got, err := v(e.callContext())
		if err != nil {
			panic(e.raise(err))
		}
		return e.toJS(got)
	}
	panic(e.vm.NewTypeError(fmt.Sprintf("the app gave the script a %T, which the bridge does not carry", value)))
}

func (e *engine) function(f Func) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		returned, err := f(e.callContext(), e.argumentsOf(call)...)
		if err != nil {
			panic(e.raise(err))
		}
		return e.toJS(returned)
	}
}

// async runs f in a goroutine of its own and settles the promise it gives the script on the loop.
func (e *engine) async(f Async) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		ctx, args := e.callContext(), e.argumentsOf(call)
		promise, resolve, reject := e.vm.NewPromise()
		e.running++
		e.work.Go(func() {
			value, err := f(ctx, args...)
			e.post(func() {
				e.running--
				e.settle(resolve, reject, value, err)
			})
		})
		return e.vm.ToValue(promise)
	}
}

func (e *engine) argumentsOf(call goja.FunctionCall) []any {
	args := make([]any, 0, len(call.Arguments))
	for i, arg := range call.Arguments {
		value, err := e.fromJS(arg, 0)
		if err != nil {
			panic(e.vm.NewTypeError(fmt.Sprintf("argument %d %v", i+1, err)))
		}
		args = append(args, value)
	}
	return args
}

// settle gives what Go answered to a promise of the script, on the loop: an error rejects it as a Func throws, and a
// value the bridge does not carry rejects it with the TypeError a Func would throw.
func (e *engine) settle(resolve, reject func(any) error, value any, err error) {
	var reason any
	if err != nil {
		reason = e.raise(err)
	} else if exception := e.vm.Try(func() { value = e.toJS(value) }); exception != nil {
		reason = exception.Value()
	}
	if reason != nil {
		err = reject(reason)
	} else {
		err = resolve(value)
	}
	// What the promise jobs cannot catch: the interrupted script or an overflowed stack.
	if err != nil {
		panic(err)
	}
}

// callContext is the context of Run with the place of the line of the script that runs.
func (e *engine) callContext() context.Context {
	at := e.placeIn(e.vm.CaptureCallStack(0, nil))
	return context.WithValue(e.ctx, placeKey{}, place{file: at.File, line: at.Line, column: at.Column})
}

// raise is the object a script catches for an error of the app: read-only, an Error, with the fields of the error.
func (e *engine) raise(err error) *goja.Object {
	var failed *FetchError
	if e.env.Error != nil && errors.As(err, &failed) {
		if translated := e.env.Error(err); translated != nil {
			err = translated
		}
	}
	var fields Map
	var fielded ScriptFields
	if errors.As(err, &fielded) {
		fields = fielded.ScriptFields()
	}
	thrown := e.vm.NewDynamicObject(&errorValue{engine: e, err: err, fields: fields, read: map[string]goja.Value{}})
	if failed := thrown.SetPrototype(e.errorPrototype); failed != nil {
		panic(failed)
	}
	e.thrown[thrown] = err
	return thrown
}

type mapValue struct {
	engine *engine
	held   Map
	read   map[string]goja.Value
}

func (m *mapValue) Get(key string) goja.Value {
	if value, read := m.read[key]; read {
		return value
	}
	value, found := m.held.Get(key)
	if !found {
		return nil
	}
	converted := m.engine.toJS(value)
	if _, computed := value.(Getter); !computed {
		m.read[key] = converted
	}
	return converted
}

func (m *mapValue) Has(key string) bool {
	_, found := m.held.Get(key)
	return found
}

func (m *mapValue) Set(string, goja.Value) bool {
	return false
}

func (m *mapValue) Delete(key string) bool {
	return !m.Has(key)
}

func (m *mapValue) Keys() []string {
	return m.held.Keys()
}

type listValue struct {
	engine *engine
	held   List
	read   map[int]goja.Value
}

func (l *listValue) Len() int {
	return l.held.Len()
}

func (l *listValue) Get(i int) goja.Value {
	if i < 0 || i >= l.held.Len() {
		return nil
	}
	if value, read := l.read[i]; read {
		return value
	}
	value := l.held.Index(i)
	converted := l.engine.toJS(value)
	if _, computed := value.(Getter); !computed {
		l.read[i] = converted
	}
	return converted
}

func (l *listValue) Set(int, goja.Value) bool {
	return false
}

func (l *listValue) SetLen(int) bool {
	return false
}

// errorValue is an error of the app as a script sees it: message, and the fields the error gives.
type errorValue struct {
	engine *engine
	err    error
	fields Map
	read   map[string]goja.Value
}

func (v *errorValue) Get(key string) goja.Value {
	if value, read := v.read[key]; read {
		return value
	}
	var value goja.Value
	if v.fields != nil {
		if field, found := v.fields.Get(key); found {
			value = v.engine.toJS(field)
		}
	}
	if value == nil && key == "message" {
		value = v.engine.vm.ToValue(v.err.Error())
	}
	if value != nil {
		v.read[key] = value
	}
	return value
}

func (v *errorValue) Has(key string) bool {
	return slices.Contains(v.Keys(), key)
}

func (v *errorValue) Set(string, goja.Value) bool {
	return false
}

func (v *errorValue) Delete(key string) bool {
	return !v.Has(key)
}

func (v *errorValue) Keys() []string {
	var keys []string
	if v.fields != nil {
		keys = v.fields.Keys()
	}
	if !slices.Contains(keys, "message") {
		keys = append([]string{"message"}, keys...)
	}
	return keys
}

// The depth a value of a script may nest to; a value that holds itself goes deeper.
const deepest = 100

// fromJS turns a value of the script into a value of the bridge. It reads getters of the script, so it runs where
// the VM catches what they throw.
func (e *engine) fromJS(value goja.Value, depth int) (any, error) {
	if depth > deepest {
		return nil, fmt.Errorf("nests deeper than %d levels, as a value that holds itself does", deepest)
	}
	switch {
	case value == nil || goja.IsUndefined(value):
		return nil, errors.New("is undefined")
	case goja.IsNull(value):
		return nil, nil
	case goja.IsString(value):
		return value.String(), nil
	case goja.IsBigInt(value):
		return value.Export().(*big.Int), nil
	case goja.IsNumber(value):
		return number(value.ToFloat()), nil
	}
	object, isObject := value.(*goja.Object)
	if !isObject {
		if set, isBool := value.Export().(bool); isBool {
			return set, nil
		}
		if _, isSymbol := value.(*goja.Symbol); isSymbol {
			return nil, errors.New("is a symbol")
		}
		return nil, fmt.Errorf("is a %s", value.ExportType())
	}
	if origin, known := e.origins[object]; known {
		return origin, nil
	}
	if object.ExportType() == bytesType {
		return slices.Clone(object.Export().([]byte)), nil
	}
	if object.ExportType() == arrayBufferType {
		return slices.Clone(object.Export().(goja.ArrayBuffer).Bytes()), nil
	}
	switch class := object.ClassName(); class {
	case "Array":
		return e.fromArray(object, depth)
	case "Object":
		if prototype := object.Prototype(); prototype == nil || prototype.SameAs(e.objectPrototype) {
			return e.fromObject(object, depth)
		}
		return nil, errors.New("is an object of a class of its own")
	case "Function":
		return nil, errors.New("is a function")
	case "Date":
		return nil, errors.New("is a Date: a moment is written as a string, such as 2026-01-01T00:00:00Z")
	default:
		return nil, fmt.Errorf("is a %s", class)
	}
}

func number(value float64) any {
	if value == math.Trunc(value) && !math.IsInf(value, 0) && value >= math.MinInt64 && value < math.MaxInt64 {
		return int64(value)
	}
	return value
}

func (e *engine) fromArray(object *goja.Object, depth int) (any, error) {
	length := object.Get("length").ToInteger()
	// The length is the script's own and may stand for no items at all, so it reserves nothing.
	var items []any
	for i := range length {
		item, err := e.fromJS(object.Get(strconv.FormatInt(i, 10)), depth+1)
		if err != nil {
			return nil, fmt.Errorf("at [%d] %w", i, err)
		}
		items = append(items, item)
	}
	if items == nil {
		items = []any{}
	}
	return items, nil
}

func (e *engine) fromObject(object *goja.Object, depth int) (any, error) {
	var keys []string
	var values []any
	for _, key := range object.Keys() {
		held := object.Get(key)
		if goja.IsUndefined(held) {
			continue
		}
		value, err := e.fromJS(held, depth+1)
		if err != nil {
			return nil, fmt.Errorf("under %s %w", quote(key), err)
		}
		keys = append(keys, key)
		values = append(values, value)
	}
	return NewObject(keys, values), nil
}

// objectOf is an *Object of the app as a frozen object of the script: a Getter is an accessor, so that reading the
// descriptors of the object computes nothing, and the rest are values converted once.
func (e *engine) objectOf(o *Object) *goja.Object {
	object := e.vm.NewObject()
	for _, key := range o.keys {
		value := o.values[key]
		var err error
		if getter, computed := value.(Getter); computed {
			get := e.vm.ToValue(func(goja.FunctionCall) goja.Value { return e.toJS(getter) })
			err = object.DefineAccessorProperty(key, get, nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
		} else {
			err = object.DefineDataProperty(key, e.toJS(value), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE)
		}
		if err != nil {
			panic(err)
		}
	}
	if _, err := e.freeze(goja.Undefined(), object); err != nil {
		panic(err)
	}
	e.origins[object] = o
	return object
}
