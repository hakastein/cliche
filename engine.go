package cliche

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja_nodejs/buffer"
)

// Node is the part of the Node subset an app gives scripts beyond Buffer, which they always have.
type Node uint8

const (
	// NodeFS is require("fs") with its *Sync functions.
	NodeFS Node = 1 << iota
	// NodeFetch is the global fetch: synchronous, GET only.
	NodeFetch
)

// Env is what a run gives a script.
type Env struct {
	// Modules are the values require gives by name.
	Modules map[string]any
	Node    Node
	// Error turns an error of cliche a script may catch (*FetchError) into an error of the app before it is thrown.
	Error func(error) error
}

type engine struct {
	ctx     context.Context
	vm      *goja.Runtime
	env     Env
	source  Source
	modules map[string]*goja.Object
	named   map[string]goja.Value
	// origins are the values of the app as the script holds them: they come back as the same values.
	origins map[*goja.Object]any
	// thrown are the objects thrown for errors of Go: an uncaught one is that error.
	thrown map[*goja.Object]error
	// Taken before any script runs, since a script may replace the globals.
	objectPrototype  *goja.Object
	errorPrototype   *goja.Object
	errorConstructor goja.Value
	dateConstructor  goja.Value
	parseJSON        goja.Callable
	buffer           *buffer.Buffer
	bufferModule     *goja.Object
	fsModule         *goja.Object
}

// goja sets no limit of its own, and a runaway recursion would take the memory of the process.
const maxCallStack = 10000

// Run runs the script of c with the call in and returns what its command returned. An error of the app the script
// left uncaught is that error; a fault of cliche is one of its error types.
func Run(ctx context.Context, c *Command, in Input, env Env) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, &InterruptedError{Cause: context.Cause(ctx)}
	}
	vm := goja.New()
	vm.SetMaxCallStackSize(maxCallStack)
	e := &engine{ctx: ctx, vm: vm, env: env, source: c.source, modules: map[string]*goja.Object{},
		named: map[string]goja.Value{}, origins: map[*goja.Object]any{}, thrown: map[*goja.Object]error{},
		objectPrototype: prototypeOf(vm, "Object"), errorPrototype: prototypeOf(vm, "Error"),
		errorConstructor: vm.Get("Error"), dateConstructor: vm.Get("Date")}
	e.parseJSON, _ = goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	e.enableNode()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(context.Cause(ctx))
		case <-stopped:
		}
	}()
	var answer any
	err := e.catch(func() { answer = e.answer(c, in) })
	if err != nil && ctx.Err() != nil {
		var interrupted *InterruptedError
		if !errors.As(err, &interrupted) {
			err = &InterruptedError{Cause: context.Cause(ctx)}
		}
	}
	if err != nil {
		return nil, err
	}
	return answer, nil
}

func prototypeOf(vm *goja.Runtime, class string) *goja.Object {
	return vm.Get(class).ToObject(vm).Get("prototype").ToObject(vm)
}

func (e *engine) enableNode() {
	module := e.vm.NewObject()
	e.set(module, "exports", e.vm.NewObject())
	buffer.Require(e.vm, module)
	e.bufferModule = module.Get("exports").ToObject(e.vm)
	constructor := e.bufferModule.Get("Buffer").ToObject(e.vm)
	// goja_nodejs leaves out Buffer.isBuffer, which scripts written for Node call.
	isBuffer := func(value goja.Value) bool {
		object, isObject := value.(*goja.Object)
		return isObject && object.ExportType() == reflect.TypeOf([]byte(nil)) && e.vm.InstanceOf(object, constructor)
	}
	e.set(constructor, "isBuffer", isBuffer)
	e.set(e.vm.GlobalObject(), "Buffer", constructor)
	if e.env.Node&NodeFetch != 0 {
		e.set(e.vm.GlobalObject(), "fetch", e.fetch)
	}
	e.buffer = buffer.GetApi(e.vm)
}

func (e *engine) set(object *goja.Object, key string, value any) {
	if err := object.Set(key, value); err != nil {
		panic(err)
	}
}

func (e *engine) define(object *goja.Object, key string, value goja.Value) {
	if err := object.DefineDataProperty(key, value, goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE); err != nil {
		panic(err)
	}
}

func (e *engine) answer(c *Command, in Input) any {
	exports, _ := e.load(c.name).(*goja.Object)
	var run goja.Callable
	if exports != nil {
		run, _ = goja.AssertFunction(exports.Get("command"))
	}
	if run == nil {
		panic(e.raise(&ScriptError{Message: "exports.command is no function, and a command script is run by " +
			"calling it", File: c.File()}))
	}
	returned, err := run(goja.Undefined(), e.arguments(in)...)
	if err != nil {
		panic(err)
	}
	answer, err := e.fromJS(returned, 0)
	if err != nil {
		panic(e.raise(&ScriptError{Message: "the value exports.command returned " + err.Error(), File: c.File()}))
	}
	return answer
}

func (e *engine) arguments(in Input) []goja.Value {
	values := make([]goja.Value, 0, len(in.Args)+1)
	for _, arg := range in.Args {
		if arg == nil {
			values = append(values, goja.Undefined())
			continue
		}
		values = append(values, e.toJS(arg))
	}
	flags := in.Flags
	if flags == nil {
		flags = NewObject(nil, nil)
	}
	return append(values, e.toJS(flags))
}

// catch turns what the script threw into an error; a stopped script and an overflowed stack pass as panics.
func (e *engine) catch(f func()) (err error) {
	defer func() {
		switch thrown := recover().(type) {
		case nil:
		case error:
			err = e.errorOf(thrown)
		default:
			panic(thrown)
		}
	}()
	if exception := e.vm.Try(f); exception != nil {
		return e.errorOf(exception)
	}
	return nil
}

func (e *engine) errorOf(err error) error {
	var interrupted *goja.InterruptedError
	var overflow *goja.StackOverflowError
	var exception *goja.Exception
	switch {
	case errors.As(err, &interrupted):
		cause := context.Cause(e.ctx)
		if cause == nil {
			cause = errors.New(interrupted.Error())
		}
		return &InterruptedError{Cause: cause}
	case errors.As(err, &overflow):
		at := e.placeIn(overflow.Stack())
		at.Message = "RangeError: Maximum call stack size exceeded"
		return at
	case errors.As(err, &exception):
		if object, isObject := exception.Value().(*goja.Object); isObject {
			if known := e.thrown[object]; known != nil {
				return known
			}
		}
		at := e.placeIn(exception.Stack())
		at.Message = e.describe(exception.Value())
		return at
	}
	panic(err)
}

// String runs the toString of a thrown object, which may throw in turn.
func (e *engine) describe(value goja.Value) string {
	text := "a value with no string form was thrown"
	e.vm.Try(func() { text = value.String() })
	return text
}

// placeIn is the first frame with a source: native frames of the app come before the line of the script.
func (e *engine) placeIn(stack []goja.StackFrame) *ScriptError {
	for _, frame := range stack {
		if at := frame.Position(); at.Line > 0 {
			return positioned("", unwrapped(at))
		}
	}
	return &ScriptError{}
}

// The wrapper keeps the lines of the module where they are; only the first line moves right by its length.
const modulePrefix = "(function (exports, require, module) {"

func unwrapped(at file.Position) file.Position {
	if at.Line == 1 {
		at.Column -= len(modulePrefix)
	}
	return at
}

func (e *engine) callerError(message string) *ScriptError {
	at := e.placeIn(e.vm.CaptureCallStack(0, nil))
	at.Message = message
	return at
}

func (e *engine) load(name string) goja.Value {
	if module, loaded := e.modules[name]; loaded {
		return module.Get("exports")
	}
	display := e.source.display(name)
	source, err := fs.ReadFile(e.source.fsys, name)
	if err != nil {
		panic(e.raise(&ScriptError{Message: fmt.Sprintf("module %s cannot be read: %v", quote(display),
			unwrapPath(err)), File: display}))
	}
	text := string(source)
	// The parser takes #! only at the start of the source, which the wrapper moves.
	if strings.HasPrefix(text, "#!") {
		text = "//" + text[2:]
	}
	program, err := parser.ParseFile(nil, display, modulePrefix+text+"\n})", 0)
	if err != nil {
		var list parser.ErrorList
		if errors.As(err, &list) && len(list) > 0 {
			panic(e.raise(positioned("SyntaxError: "+list[0].Message, unwrapped(list[0].Position))))
		}
		panic(e.raise(&ScriptError{Message: err.Error(), File: display}))
	}
	compiled, err := goja.CompileAST(program, true)
	var syntax *goja.CompilerSyntaxError
	if errors.As(err, &syntax) && syntax.File != nil {
		panic(e.raise(positioned("SyntaxError: "+syntax.Message, unwrapped(syntax.File.Position(syntax.Offset)))))
	}
	if err != nil {
		panic(e.raise(&ScriptError{Message: err.Error(), File: display}))
	}
	wrapper, err := e.vm.RunProgram(compiled)
	if err != nil {
		panic(err)
	}
	module, exports := e.vm.NewObject(), e.vm.NewObject()
	e.set(module, "exports", exports)
	e.modules[name] = module
	call, _ := goja.AssertFunction(wrapper)
	if _, err := call(goja.Undefined(), exports, e.vm.ToValue(e.require(name)), module); err != nil {
		delete(e.modules, name)
		panic(err)
	}
	return module.Get("exports")
}

func (e *engine) require(from string) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if !goja.IsString(call.Argument(0)) {
			panic(e.vm.NewTypeError("require takes a string"))
		}
		wanted := call.Argument(0).String()
		if value, held := e.env.Modules[wanted]; held {
			if module, built := e.named[wanted]; built {
				return module
			}
			module := e.toJS(value)
			e.named[wanted] = module
			return module
		}
		switch {
		case (wanted == "fs" || wanted == "node:fs") && e.env.Node&NodeFS != 0:
			if e.fsModule == nil {
				e.fsModule = e.nodeFS()
			}
			return e.fsModule
		case wanted == "buffer", wanted == "node:buffer":
			return e.bufferModule
		case strings.HasPrefix(wanted, "./"), strings.HasPrefix(wanted, "../"):
			return e.load(e.library(from, wanted))
		}
		panic(e.raise(e.callerError(fmt.Sprintf("require %s names neither a module of the app, a module of Node "+
			"the app gives, nor a module by a path starting with ./ or ../", quote(wanted)))))
	}
}

func (e *engine) library(from, wanted string) string {
	name := path.Join(path.Dir(from), wanted)
	if !strings.HasSuffix(name, ".js") {
		name += ".js"
	}
	if name == ".." || strings.HasPrefix(name, "../") {
		panic(e.raise(e.callerError(fmt.Sprintf("require %s leaves the source of the script, and a script takes "+
			"modules only from its own source", quote(wanted)))))
	}
	source, err := fs.ReadFile(e.source.fsys, name)
	if err != nil {
		panic(e.raise(e.callerError(fmt.Sprintf("require %s: %v", quote(wanted), unwrapPath(err)))))
	}
	if !strings.Contains(string(source), "definition") {
		return name
	}
	program, err := parser.ParseFile(nil, e.source.display(name), string(source), 0)
	if err == nil && commandLiteral(program) != nil {
		panic(e.raise(e.callerError(fmt.Sprintf("require %s names a command script, and a command calls no other "+
			"command", quote(wanted)))))
	}
	return name
}
