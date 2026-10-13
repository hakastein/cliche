package cliche

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
)

// Command is a script command, read from its declaration alone, without running the module.
type Command struct {
	Path  []string
	Short string
	Long  string
	Args  []Arg
	Flags []Flag

	source Source
	// name is the path of the file inside its source.
	name  string
	types map[string]Type
}

// Builtin tells a script of Config.Builtin from one of a source.
func (c *Command) Builtin() bool {
	return c.source.builtin
}

// File names the script as faults and Place name it.
func (c *Command) File() string {
	return c.source.display(c.name)
}

// Arg is one element of the call: a single value of Type, or a group of Slots.
type Arg struct {
	Name     string
	Type     string
	Usage    string
	Group    []Slot
	Optional bool
	Multiple bool
}

// Slot is one value of a group.
type Slot struct {
	Name  string
	Type  string
	Usage string
}

// Flag is a flag of the call. Default is nil when the declaration gives none.
type Flag struct {
	Name     string
	Type     string
	Usage    string
	Multiple bool
	Choices  []string
	Default  any
}

func (a Arg) width() int {
	if a.Group != nil {
		return len(a.Group)
	}
	return 1
}

func (a Arg) variable() bool {
	return a.Optional || a.Multiple
}

// position turns a place of the parser into one of the file, whose first line the module wrapper moves right.
func positioned(message string, at file.Position) *ScriptError {
	return &ScriptError{Message: message, File: at.Filename, Line: at.Line, Column: at.Column}
}

// declaration reads exports.definition of a module; a module without one is a library module, and nil, nil stands
// for it.
func declaration(name, source string, types map[string]Type) (*Command, *ScriptError) {
	if strings.HasPrefix(source, "#!") {
		source = "//" + source[2:]
	}
	program, err := parser.ParseFile(nil, name, source, 0)
	if err != nil {
		var list parser.ErrorList
		if errors.As(err, &list) && len(list) > 0 {
			return nil, positioned("SyntaxError: "+list[0].Message, list[0].Position)
		}
		return nil, &ScriptError{Message: err.Error(), File: name}
	}
	literal := commandLiteral(program)
	if literal == nil {
		return nil, nil
	}
	r := &literalReader{file: program.File, types: types}
	value := r.value(literal)
	if r.fault != nil {
		return nil, r.fault
	}
	command := r.command(value, literal)
	if r.fault != nil {
		return nil, r.fault
	}
	command.types = types
	return command, nil
}

func commandLiteral(program *ast.Program) ast.Expression {
	for _, statement := range program.Body {
		expression, isExpression := statement.(*ast.ExpressionStatement)
		if !isExpression {
			continue
		}
		assignment, isAssignment := expression.Expression.(*ast.AssignExpression)
		if !isAssignment || assignment.Operator != token.ASSIGN {
			continue
		}
		target, isDot := assignment.Left.(*ast.DotExpression)
		if !isDot || target.Identifier.Name != "definition" {
			continue
		}
		if exports, isName := target.Left.(*ast.Identifier); isName && exports.Name == "exports" {
			return assignment.Right
		}
	}
	return nil
}

// literal is a value of the declaration: string, float64, bool, nil, []literal or *object.
type literal any

type object struct {
	keys   []string
	values map[string]literal
	nodes  map[string]ast.Expression
}

type literalReader struct {
	file  *file.File
	types map[string]Type
	fault *ScriptError
}

func (r *literalReader) fail(at ast.Node, format string, args ...any) {
	if r.fault != nil {
		return
	}
	r.fault = positioned("exports.definition "+fmt.Sprintf(format, args...),
		r.file.Position(int(at.Idx0())-r.file.Base()))
}

func (r *literalReader) value(expression ast.Expression) literal {
	switch e := expression.(type) {
	case *ast.StringLiteral:
		return e.Value.String()
	case *ast.TemplateLiteral:
		if e.Tag == nil && len(e.Expressions) == 0 && len(e.Elements) == 1 {
			return e.Elements[0].Parsed.String()
		}
	case *ast.NumberLiteral:
		if number, isFloat := e.Value.(float64); isFloat {
			return number
		}
		if number, isInt := e.Value.(int64); isInt {
			return float64(number)
		}
	case *ast.BooleanLiteral:
		return e.Value
	case *ast.NullLiteral:
		return nil
	case *ast.ArrayLiteral:
		items := make([]literal, 0, len(e.Value))
		for _, item := range e.Value {
			if item == nil {
				r.fail(e, "holds an array with a hole")
				return nil
			}
			items = append(items, r.value(item))
		}
		return items
	case *ast.ObjectLiteral:
		return r.object(e)
	case *ast.BinaryExpression:
		if joined, isText := r.joined(e); isText {
			return joined
		}
	}
	r.fail(expression, "is no pure literal: it may hold strings, strings joined by +, numbers, true, false, null, "+
		"arrays and objects, but no names, calls or other computations")
	return nil
}

func (r *literalReader) joined(e *ast.BinaryExpression) (string, bool) {
	if e.Operator != token.PLUS {
		return "", false
	}
	var parts [2]string
	for i, side := range []ast.Expression{e.Left, e.Right} {
		switch side.(type) {
		case *ast.StringLiteral, *ast.TemplateLiteral, *ast.BinaryExpression:
		default:
			return "", false
		}
		text, isText := r.value(side).(string)
		if !isText {
			return "", false
		}
		parts[i] = text
	}
	return parts[0] + parts[1], true
}

func (r *literalReader) object(e *ast.ObjectLiteral) *object {
	read := &object{values: map[string]literal{}, nodes: map[string]ast.Expression{}}
	for _, property := range e.Value {
		keyed, isKeyed := property.(*ast.PropertyKeyed)
		if !isKeyed || keyed.Computed || keyed.Kind != ast.PropertyKindValue {
			r.fail(property, "holds a property that is no plain key and value")
			return read
		}
		var key string
		switch name := keyed.Key.(type) {
		case *ast.StringLiteral:
			key = name.Value.String()
		case *ast.Identifier:
			key = name.Name.String()
		default:
			r.fail(keyed.Key, "holds a key that is neither a name nor a string")
			return read
		}
		if _, repeated := read.values[key]; repeated {
			r.fail(keyed.Key, "holds the key %s twice", quote(key))
			return read
		}
		read.keys = append(read.keys, key)
		read.values[key] = r.value(keyed.Value)
		read.nodes[key] = keyed.Value
	}
	return read
}

const maxShort = 60

func (r *literalReader) command(value literal, at ast.Expression) *Command {
	declared, isObject := value.(*object)
	if !isObject {
		r.fail(at, "is no object")
		return nil
	}
	r.onlyKeys(declared, at, "short", "long", "args", "flags")
	command := &Command{}
	command.Short = r.text(declared, "short", at)
	if short := command.Short; r.fault == nil && (short == "" || strings.ContainsAny(short, "\r\n") ||
		len([]rune(short)) > maxShort) {
		r.fail(declared.nodes["short"], "short is not one line of 1 to %d characters", maxShort)
	}
	command.Long = r.text(declared, "long", at)
	command.Args = r.args(declared)
	command.Flags = r.flags(declared)
	argNames := []string{}
	variable := 0
	for _, arg := range command.Args {
		argNames = append(argNames, arg.Name)
		if arg.variable() {
			variable++
		}
	}
	if variable > 1 {
		r.fail(declared.nodes["args"], "declares %d arguments that are optional or multiple, and a call is read "+
			"unambiguously with one at most", variable)
	}
	flagNames := []string{"help"}
	for _, flag := range command.Flags {
		flagNames = append(flagNames, flag.Name)
	}
	for _, names := range [][]string{argNames, flagNames} {
		for i, name := range names {
			if slices.Contains(names[:i], name) {
				r.fail(at, "names %s twice", quote(name))
			}
		}
	}
	return command
}

func (r *literalReader) onlyKeys(declared *object, at ast.Node, allowed ...string) {
	for _, key := range declared.keys {
		if !slices.Contains(allowed, key) {
			r.fail(at, "holds %s, which is none of %s", quote(key), strings.Join(allowed, ", "))
		}
	}
}

func (r *literalReader) text(declared *object, key string, at ast.Node) string {
	value, given := declared.values[key]
	if !given {
		r.fail(at, "has no %s", key)
		return ""
	}
	text, isString := value.(string)
	if !isString {
		r.fail(declared.nodes[key], "%s is no string", key)
	}
	return text
}

func (r *literalReader) flag(declared *object, key string) bool {
	value, given := declared.values[key]
	if !given {
		return false
	}
	set, isBool := value.(bool)
	if !isBool {
		r.fail(declared.nodes[key], "%s is neither true nor false", key)
	}
	return set
}

func (r *literalReader) items(declared *object, key string, allowed ...string) ([]*object, []ast.Expression) {
	value, given := declared.values[key]
	if !given {
		return nil, nil
	}
	listed, isArray := value.([]literal)
	if !isArray {
		r.fail(declared.nodes[key], "%s is no array", key)
		return nil, nil
	}
	at := declared.nodes[key].(*ast.ArrayLiteral).Value
	read := make([]*object, 0, len(listed))
	for i, item := range listed {
		held, isObject := item.(*object)
		if !isObject {
			r.fail(at[i], "%s holds an item that is no object", key)
			return nil, nil
		}
		r.onlyKeys(held, at[i], allowed...)
		read = append(read, held)
	}
	return read, at
}

func (r *literalReader) name(declared *object, at ast.Node) string {
	name := r.text(declared, "name", at)
	if r.fault == nil && !wordGrammar.MatchString(name) {
		r.fail(declared.nodes["name"], "names %s, which is not %s", quote(name), wordRule)
	}
	return name
}

var (
	argTypes  = []string{typeString, typePath, typeInt, typePair, typeDuration}
	flagTypes = []string{typeString, typePath, typeInt, typeBool, typePair, typeDuration}
)

// kind reads the type of an argument, a slot or a flag, and its usage: the usage of an app type is its own.
func (r *literalReader) kind(declared *object, at ast.Node, base []string, what string) (string, string) {
	kind := r.text(declared, "type", at)
	if r.fault != nil {
		return kind, ""
	}
	app, isApp := r.types[kind]
	if !slices.Contains(base, kind) && !isApp || slices.Contains(flagTypes, kind) && !slices.Contains(base, kind) {
		r.fail(declared.nodes["type"], "gives %s the type %s, which is none of %s or a type of the app", what,
			quote(kind), strings.Join(base, ", "))
		return kind, ""
	}
	if slices.Contains(base, kind) {
		return kind, r.text(declared, "usage", at)
	}
	if _, given := declared.values["usage"]; given {
		r.fail(declared.nodes["usage"], "gives %s a usage, and the usage of the type %s is its own", what, quote(kind))
	}
	return kind, app.Usage
}

func (r *literalReader) args(declared *object) []Arg {
	items, at := r.items(declared, "args", "name", "type", "usage", "group", "optional", "multiple")
	args := make([]Arg, 0, len(items))
	for i, held := range items {
		arg := Arg{Name: r.name(held, at[i])}
		what := "argument " + quote(arg.Name)
		if _, isGroup := held.values["group"]; isGroup {
			if _, typed := held.values["type"]; typed {
				r.fail(held.nodes["type"], "gives %s both a type and a group", what)
			}
			arg.Usage = r.text(held, "usage", at[i])
			arg.Group = r.slots(held, what)
		} else {
			arg.Type, arg.Usage = r.kind(held, at[i], argTypes, what)
		}
		arg.Optional = r.flag(held, "optional")
		arg.Multiple = r.flag(held, "multiple")
		args = append(args, arg)
	}
	return args
}

func (r *literalReader) slots(declared *object, what string) []Slot {
	items, at := r.items(declared, "group", "name", "type", "usage")
	if r.fault == nil && len(items) == 0 {
		r.fail(declared.nodes["group"], "gives %s a group of no slots", what)
	}
	slots := make([]Slot, 0, len(items))
	var names []string
	for i, held := range items {
		slot := Slot{Name: r.name(held, at[i])}
		slot.Type, slot.Usage = r.kind(held, at[i], argTypes, "slot "+quote(slot.Name)+" of "+what)
		if slices.Contains(names, slot.Name) {
			r.fail(at[i], "names the slot %s of %s twice", quote(slot.Name), what)
		}
		names = append(names, slot.Name)
		slots = append(slots, slot)
	}
	return slots
}

func (r *literalReader) flags(declared *object) []Flag {
	items, at := r.items(declared, "flags", "name", "type", "multiple", "usage", "choices", "default")
	flags := make([]Flag, 0, len(items))
	for i, held := range items {
		flag := Flag{Name: r.name(held, at[i])}
		flag.Type, flag.Usage = r.kind(held, at[i], flagTypes, "--"+flag.Name)
		flag.Multiple = r.flag(held, "multiple")
		if flag.Multiple && flag.Type != typeString && flag.Type != typePair {
			r.fail(held.nodes["multiple"], "gives --%s multiple, and only a string or a pair flag is given more "+
				"than once", flag.Name)
		}
		flag.Choices = r.choices(held, flag)
		flag.Default = r.flagDefault(held, flag, at[i])
		flags = append(flags, flag)
	}
	return flags
}

func (r *literalReader) choices(held *object, flag Flag) []string {
	value, given := held.values["choices"]
	if !given {
		return nil
	}
	at := held.nodes["choices"]
	if flag.Type != typeString {
		r.fail(at, "gives choices to --%s, which is no string flag", flag.Name)
		return nil
	}
	choices, isStrings := stringsOf(value)
	if !isStrings || len(choices) == 0 {
		r.fail(at, "gives --%s choices that are no array of strings", flag.Name)
	}
	return choices
}

func stringsOf(value literal) ([]string, bool) {
	items, isArray := value.([]literal)
	texts := make([]string, 0, len(items))
	for _, item := range items {
		text, isString := item.(string)
		if !isString {
			return nil, false
		}
		texts = append(texts, text)
	}
	return texts, isArray
}

func (r *literalReader) flagDefault(held *object, flag Flag, at ast.Node) any {
	value, given := held.values["default"]
	app, isApp := r.types[flag.Type]
	isApp = isApp && !slices.Contains(flagTypes, flag.Type)
	switch {
	case !given && isApp && app.Default:
		r.fail(at, "gives --%s no default, and the type %s takes one", flag.Name, quote(flag.Type))
		return nil
	case !given:
		return nil
	case flag.Type == typePair || flag.Type == typeDuration:
		r.fail(held.nodes["default"], "gives --%s a default, and a %s flag has none", flag.Name, flag.Type)
		return nil
	case isApp:
		return bridgeOf(value)
	}
	read, fits := defaultOf(flag, value)
	switch {
	case !fits && flag.Multiple:
		r.fail(held.nodes["default"], "gives --%s a default that is no array of strings", flag.Name)
	case !fits:
		r.fail(held.nodes["default"], "gives --%s a default that is no %s", flag.Name, flag.Type)
	case !chosen(flag.Choices, read):
		r.fail(held.nodes["default"], "gives --%s a default outside its choices", flag.Name)
	}
	return read
}

func defaultOf(flag Flag, value literal) (any, bool) {
	if flag.Multiple {
		texts, isStrings := stringsOf(value)
		items := make([]any, 0, len(texts))
		for _, text := range texts {
			items = append(items, text)
		}
		return items, isStrings
	}
	switch flag.Type {
	case typeString, typePath:
		text, isString := value.(string)
		return text, isString
	case typeInt:
		number, isNumber := value.(float64)
		return int64(number), isNumber && number == math.Trunc(number) && number >= math.MinInt32 &&
			number <= math.MaxInt32
	case typeBool:
		set, isBool := value.(bool)
		return set, isBool
	}
	return nil, false
}

func chosen(choices []string, value any) bool {
	if len(choices) == 0 {
		return true
	}
	items, isList := value.([]any)
	if !isList {
		items = []any{value}
	}
	for _, item := range items {
		if !slices.Contains(choices, item.(string)) {
			return false
		}
	}
	return true
}

// bridgeOf turns a literal into a value of the bridge.
func bridgeOf(value literal) any {
	switch v := value.(type) {
	case float64:
		if v == math.Trunc(v) && math.Abs(v) <= exactInDouble {
			return int64(v)
		}
		return v
	case []literal:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, bridgeOf(item))
		}
		return items
	case *object:
		values := make([]any, 0, len(v.keys))
		for _, key := range v.keys {
			values = append(values, bridgeOf(v.values[key]))
		}
		return NewObject(v.keys, values)
	}
	return value
}
