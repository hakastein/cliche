package cliche

import (
	"fmt"
)

// ScriptError is a fault of a script: an exception it left uncaught, an unreadable declaration, a broken path, or a
// value it returned that the bridge does not carry. File, Line and Column are the place when it is known.
type ScriptError struct {
	Message string
	File    string
	Line    int
	Column  int
}

func (e *ScriptError) Error() string {
	switch {
	case e.File != "" && e.Line > 0:
		return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Column, e.Message)
	case e.File != "":
		return e.File + ": " + e.Message
	}
	return e.Message
}

// UsageError is a call of a script command that its declaration does not take; the script did not run.
type UsageError struct {
	Message string
}

func (e *UsageError) Error() string {
	return e.Message
}

// FileError is an error of fs that the script left uncaught: the Node code, the syscall and the path.
type FileError struct {
	Message string
	Code    string
	Syscall string
	Path    string
}

func (e *FileError) Error() string {
	return e.Message
}

// FetchError is a fetch that got no whole response: a failed transport, a timeout, a redirect too many, or a body
// longer than maxBytes. A response of any status is no error.
type FetchError struct {
	URL    string
	Reason string
}

func (e *FetchError) Error() string {
	return "GET " + e.URL + ": " + e.Reason
}

// ScriptFields gives a script the url of the request it caught.
func (e *FetchError) ScriptFields() Map {
	return NewObject([]string{"message", "url"}, []any{e.Error(), e.URL})
}

// InterruptedError is a script stopped by the cancellation of its context; Cause is the cause of that context.
type InterruptedError struct {
	Cause error
}

func (e *InterruptedError) Error() string {
	return "the script was stopped: " + e.Cause.Error()
}

func (e *InterruptedError) Unwrap() error {
	return e.Cause
}

// ScriptFields is implemented by an error of the app that a script reads fields of in a catch block.
type ScriptFields interface {
	ScriptFields() Map
}

func quote(text string) string {
	return "`" + text + "`"
}
