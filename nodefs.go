package cliche

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/buffer"
)

func (e *engine) nodeFS() *goja.Object {
	module := e.vm.NewObject()
	functions := []struct {
		name     string
		function func(goja.FunctionCall) goja.Value
	}{
		{"readFileSync", e.readFileSync},
		{"writeFileSync", e.writeFileSync},
		{"mkdirSync", e.mkdirSync},
		{"existsSync", e.existsSync},
		{"readdirSync", e.readdirSync},
		{"statSync", e.statSync},
	}
	for _, f := range functions {
		e.define(module, f.name, e.vm.ToValue(f.function))
	}
	return module
}

func (e *engine) pathArgument(call goja.FunctionCall) string {
	if !goja.IsString(call.Argument(0)) {
		panic(e.vm.NewTypeError(`The "path" argument must be of type string`))
	}
	return call.Argument(0).String()
}

// An option Node has and cliche does not is refused: ignored, it would do silently what the script did not ask for.
func (e *engine) optionsArgument(value goja.Value, takes ...string) *goja.Object {
	if goja.IsUndefined(value) || goja.IsNull(value) {
		return e.vm.NewObject()
	}
	options, isObject := value.(*goja.Object)
	if !isObject || options.ClassName() != "Object" {
		panic(e.vm.NewTypeError(`The "options" argument must be of type object`))
	}
	for _, key := range options.Keys() {
		if !slices.Contains(takes, key) {
			panic(e.vm.NewTypeError(fmt.Sprintf("the option %s is not one fs takes here: it takes %v", quote(key),
				takes)))
		}
	}
	return options
}

func (e *engine) encodingArgument(value goja.Value) goja.Value {
	if goja.IsString(value) {
		return e.checkedEncoding(value)
	}
	return e.checkedEncoding(option(e.optionsArgument(value, "encoding"), "encoding"))
}

func option(options *goja.Object, key string) goja.Value {
	if value := options.Get(key); value != nil {
		return value
	}
	return goja.Undefined()
}

func (e *engine) checkedEncoding(encoding goja.Value) goja.Value {
	if goja.IsUndefined(encoding) || goja.IsNull(encoding) {
		return goja.Undefined()
	}
	if buffer.StringCodecByName(encoding.String()) == nil {
		panic(e.vm.NewTypeError(fmt.Sprintf("The argument 'encoding' is invalid encoding. Received '%s'",
			encoding.String())))
	}
	return encoding
}

func (e *engine) readFileSync(call goja.FunctionCall) goja.Value {
	path := e.pathArgument(call)
	encoding := e.encodingArgument(call.Argument(1))
	file, err := openRegular(path)
	var irregular *irregularFile
	if errors.As(err, &irregular) && irregular.mode.IsDir() {
		// Node opens a directory and fails on reading it.
		panic(e.systemError(err, "read", path))
	}
	if err != nil {
		panic(e.systemError(err, "open", path))
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		panic(e.systemError(err, "read", path))
	}
	if goja.IsUndefined(encoding) {
		return e.buffer.WrapBytes(content)
	}
	return e.vm.ToValue(buffer.StringCodecByName(encoding.String()).Encode(content))
}

func (e *engine) writeFileSync(call goja.FunctionCall) goja.Value {
	path := e.pathArgument(call)
	encoding := e.encodingArgument(call.Argument(2))
	content, isBytes := byteArray(call.Argument(1))
	if goja.IsString(call.Argument(1)) {
		content, isBytes = buffer.DecodeBytes(e.vm, call.Argument(1), encoding), true
	}
	if !isBytes {
		panic(e.vm.NewTypeError(`The "data" argument must be of type string or an instance of Buffer or Uint8Array`))
	}
	// A named pipe would hold the write until a reader appears.
	if found, err := os.Stat(path); err == nil && !found.Mode().IsRegular() {
		panic(e.systemError(&irregularFile{mode: found.Mode()}, "open", path))
	}
	if err := os.WriteFile(path, content, 0o666); err != nil {
		panic(e.systemError(err, "open", path))
	}
	return goja.Undefined()
}

func (e *engine) mkdirSync(call goja.FunctionCall) goja.Value {
	path := e.pathArgument(call)
	if !option(e.optionsArgument(call.Argument(1), "recursive"), "recursive").ToBoolean() {
		if err := os.Mkdir(path, 0o777); err != nil {
			panic(e.systemError(err, "mkdir", path))
		}
		return goja.Undefined()
	}
	// MkdirAll reports a file in the way as ENOTDIR, and Node as EEXIST.
	if found, err := os.Stat(path); err == nil && !found.IsDir() {
		panic(e.systemError(fs.ErrExist, "mkdir", path))
	}
	first := ""
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(dir); err == nil || filepath.Dir(dir) == dir {
			break
		}
		first = dir
	}
	if err := os.MkdirAll(path, 0o777); err != nil {
		panic(e.systemError(err, "mkdir", path))
	}
	if first == "" {
		return goja.Undefined()
	}
	return e.vm.ToValue(first)
}

func (e *engine) existsSync(call goja.FunctionCall) goja.Value {
	if !goja.IsString(call.Argument(0)) {
		return e.vm.ToValue(false)
	}
	_, err := os.Stat(call.Argument(0).String())
	return e.vm.ToValue(err == nil)
}

func (e *engine) readdirSync(call goja.FunctionCall) goja.Value {
	path := e.pathArgument(call)
	if encoding := e.encodingArgument(call.Argument(1)); !goja.IsUndefined(encoding) &&
		!slices.Contains([]string{"utf8", "utf-8"}, encoding.String()) {
		panic(e.vm.NewTypeError("readdirSync gives the names in utf8 only"))
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		panic(e.systemError(err, "scandir", path))
	}
	names := make([]any, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return e.vm.NewArray(names...)
}

func (e *engine) statSync(call goja.FunctionCall) goja.Value {
	path := e.pathArgument(call)
	throwIfNoEntry := option(e.optionsArgument(call.Argument(1), "throwIfNoEntry"), "throwIfNoEntry")
	found, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) && !goja.IsUndefined(throwIfNoEntry) && !throwIfNoEntry.ToBoolean() {
		return goja.Undefined()
	}
	if err != nil {
		panic(e.systemError(err, "stat", path))
	}
	changed := float64(found.ModTime().UnixNano()) / 1e6
	mtime, err := e.vm.New(e.dateConstructor, e.vm.ToValue(changed))
	if err != nil {
		panic(err)
	}
	stats := e.vm.NewObject()
	e.set(stats, "size", found.Size())
	e.set(stats, "mtimeMs", changed)
	e.set(stats, "mtime", mtime)
	e.set(stats, "isFile", func() bool { return found.Mode().IsRegular() })
	e.set(stats, "isDirectory", func() bool { return found.IsDir() })
	e.set(stats, "isSymbolicLink", func() bool { return false })
	return stats
}

type systemCode struct {
	errno       error
	code        string
	description string
}

// The codes and the words of libuv, which Node prints.
var systemCodes = []systemCode{
	{syscall.ENOENT, "ENOENT", "no such file or directory"},
	{syscall.EISDIR, "EISDIR", "illegal operation on a directory"},
	{syscall.ENOTDIR, "ENOTDIR", "not a directory"},
	{syscall.EEXIST, "EEXIST", "file already exists"},
	{syscall.ENOTEMPTY, "ENOTEMPTY", "directory not empty"},
	{syscall.EACCES, "EACCES", "permission denied"},
	{syscall.EPERM, "EPERM", "operation not permitted"},
	{syscall.ELOOP, "ELOOP", "too many symbolic links encountered"},
	{syscall.ENAMETOOLONG, "ENAMETOOLONG", "name too long"},
	{syscall.ENOSPC, "ENOSPC", "no space left on device"},
	{syscall.EROFS, "EROFS", "read-only file system"},
	{fs.ErrNotExist, "ENOENT", "no such file or directory"},
	{fs.ErrExist, "EEXIST", "file already exists"},
	{fs.ErrPermission, "EACCES", "permission denied"},
}

// systemError is an Error as Node throws it, with code, syscall and path; left uncaught, it is a *FileError.
func (e *engine) systemError(err error, operation, path string) *goja.Object {
	code, description := "EIO", unwrapPath(err).Error()
	var irregular *irregularFile
	switch {
	case errors.As(err, &irregular) && irregular.mode.IsDir():
		code, description = "EISDIR", "illegal operation on a directory"
	case errors.As(err, &irregular), errors.Is(err, errSwapped):
		code, description = "EINVAL", "path "+err.Error()+", and fs reads and writes only a regular file"
	default:
		known := append(slices.Clone(platformCodes), systemCodes...)
		at := slices.IndexFunc(known, func(candidate systemCode) bool { return errors.Is(err, candidate.errno) })
		if at >= 0 {
			code, description = known[at].code, known[at].description
		}
	}
	message := fmt.Sprintf("%s: %s, %s '%s'", code, description, operation, path)
	thrown, failed := e.vm.New(e.errorConstructor, e.vm.ToValue(message))
	if failed != nil {
		panic(failed)
	}
	e.set(thrown, "code", code)
	e.set(thrown, "syscall", operation)
	e.set(thrown, "path", path)
	e.thrown[thrown] = &FileError{Message: message, Code: code, Syscall: operation, Path: path}
	return thrown
}

type irregularFile struct {
	mode fs.FileMode
}

func (i *irregularFile) Error() string {
	switch mode := i.mode; {
	case mode.IsDir():
		return "is a directory"
	case mode&fs.ModeNamedPipe != 0:
		return "is a named pipe"
	case mode&fs.ModeSocket != 0:
		return "is a socket"
	case mode&fs.ModeCharDevice != 0:
		return "is a character device"
	case mode&fs.ModeDevice != 0:
		return "is a block device"
	}
	return "is no regular file"
}

var errSwapped = errors.New("is no longer the file it stood for a moment ago")

func openRegular(path string) (*os.File, error) {
	// Stat before Open: opening a named pipe blocks until a writer appears.
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, &irregularFile{mode: before.Mode()}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, errSwapped
	}
	return file, nil
}

// A Buffer is a Uint8Array, and both export as a byte slice.
func byteArray(value goja.Value) ([]byte, bool) {
	object, isObject := value.(*goja.Object)
	if !isObject || object.ExportType() != reflect.TypeOf([]byte(nil)) {
		return nil, false
	}
	return slices.Clone(object.Export().([]byte)), true
}
