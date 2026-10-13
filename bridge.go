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
		args := make([]any, 0, len(call.Arguments))
		for i, arg := range call.Arguments {
			value, err := e.fromJS(arg, 0)
			if err != nil {
				panic(e.vm.NewTypeError(fmt.Sprintf("argument %d %v", i+1, err)))
			}
			args = append(args, value)
		}
		returned, err := f(e.callContext(), args...)
		if err != nil {
			panic(e.raise(err))
		}
		return e.toJS(returned)
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
	items := make([]any, 0, length)
	for i := range length {
		item, err := e.fromJS(object.Get(strconv.FormatInt(i, 10)), depth+1)
		if err != nil {
			return nil, fmt.Errorf("at [%d] %w", i, err)
		}
		items = append(items, item)
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
