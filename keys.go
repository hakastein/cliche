package cliche

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dop251/goja/ast"
)

// builtinKeys are the keys of a declaration that cliche reads itself; no key of the app is one of them.
var builtinKeys = []string{"short", "long", "args", "flags"}

// Key is a key of the app in exports.definition beside short, long, args and flags. cliche reads its value as the
// same pure literal as the rest of the declaration, without running the module; what the value may be is the app's.
type Key struct {
	// Required makes the key required of every declaration.
	Required bool
	// Read checks the value and gives what Command.Keys holds for the key. An error makes the declaration unreadable:
	// an error of Literal.Errorf names the place of that literal, any other error the value of the key. Read nil keeps
	// the Literal itself.
	Read func(value Literal) (any, error)
}

// Literal is a value of a declaration and its place in the script. Value is a string, a float64, a bool, nil, a
// []Literal or a *LiteralObject.
type Literal struct {
	Line, Column int
	Value        any
}

// LiteralObject is an object of a declaration: its keys in the order written and the value of each.
type LiteralObject struct {
	Keys   []string
	Values map[string]Literal
}

// Errorf is the error by which Key.Read refuses l: the *ScriptError of the declaration names the place of l.
func (l Literal) Errorf(format string, args ...any) error {
	return &literalError{line: l.Line, column: l.Column, message: fmt.Sprintf(format, args...)}
}

type literalError struct {
	line, column int
	message      string
}

func (e *literalError) Error() string {
	return e.message
}

// checkKeys panics on a key of the app that cliche reads itself.
func checkKeys(keys map[string]Key) {
	for name := range keys {
		if slices.Contains(builtinKeys, name) {
			panic(fmt.Sprintf("cliche: Config.Keys holds %s, which is a key cliche reads itself: %s", quote(name),
				strings.Join(builtinKeys, ", ")))
		}
	}
}

func sortedKeys(keys map[string]Key) []string {
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// appKeys reads the keys of the app that the declaration gives; nil stands for an app with no keys.
func (r *literalReader) appKeys(declared *object, at ast.Node) map[string]any {
	if len(r.keys) == 0 || r.fault != nil {
		return nil
	}
	read := map[string]any{}
	for _, name := range sortedKeys(r.keys) {
		key := r.keys[name]
		node, given := declared.nodes[name]
		if !given {
			if key.Required {
				r.fail(at, "has no %s", name)
				return nil
			}
			continue
		}
		value := r.view(declared.values[name], node)
		if key.Read == nil {
			read[name] = value
			continue
		}
		held, err := key.Read(value)
		if err != nil {
			r.refuse(name, node, err)
			return nil
		}
		read[name] = held
	}
	return read
}

func (r *literalReader) refuse(name string, at ast.Node, err error) {
	var placed *literalError
	if !errors.As(err, &placed) {
		r.fail(at, "gives %s a value the app does not take: %v", name, err)
		return
	}
	r.fault = &ScriptError{Message: fmt.Sprintf("exports.definition gives %s a value the app does not take: %v", name,
		err), File: r.file.Name(), Line: placed.line, Column: placed.column}
}

// view gives the app a literal of the declaration with the places of its values.
func (r *literalReader) view(value literal, at ast.Expression) Literal {
	line, column := r.place(at)
	switch v := value.(type) {
	case []literal:
		elements := at.(*ast.ArrayLiteral).Value
		items := make([]Literal, 0, len(v))
		for i, item := range v {
			items = append(items, r.view(item, elements[i]))
		}
		return Literal{Line: line, Column: column, Value: items}
	case *object:
		values := make(map[string]Literal, len(v.keys))
		for _, key := range v.keys {
			values[key] = r.view(v.values[key], v.nodes[key])
		}
		return Literal{Line: line, Column: column, Value: &LiteralObject{Keys: slices.Clone(v.keys), Values: values}}
	}
	return Literal{Line: line, Column: column, Value: value}
}
