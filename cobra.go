package cliche

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Input is a parsed call of a script command: the arguments in their declared order and the flags.
type Input struct {
	// Args holds a value per declared argument: a single one is its value, a group a *Object of its slots, a multiple
	// one a []any; an optional one not given is Undefined.
	Args []any
	// Flags holds the flags given and the ones with a default.
	Flags *Object
}

// Cobra builds the cobra command of c. run gets the parsed call; a call the declaration does not take is a
// *UsageError before run.
func (c *Command) Cobra(run func(cmd *cobra.Command, in Input) error) *cobra.Command {
	values := make([]*flagText, 0, len(c.Flags))
	cmd := &cobra.Command{
		Use:   c.use(),
		Short: c.Short,
		Long:  c.long(),
		Args: func(_ *cobra.Command, args []string) error {
			_, err := c.split(args)
			return err
		},
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]cobra.Completion,
			cobra.ShellCompDirective) {
			if c.pathAt(len(args)) {
				return nil, cobra.ShellCompDirectiveDefault
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := c.input(args, values)
			if err != nil {
				return err
			}
			return run(cmd, in)
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &UsageError{Message: err.Error()}
	})
	for _, flag := range c.Flags {
		value := &flagText{types: c.types, flag: flag}
		values = append(values, value)
		cmd.Flags().Var(value, flag.Name, flag.Usage)
		declared := cmd.Flags().Lookup(flag.Name)
		if flag.Type == typeBool {
			declared.NoOptDefVal = "true"
		}
		if flag.Default != nil {
			declared.DefValue = defaultText(flag.Default)
		}
		directive := cobra.ShellCompDirectiveNoFileComp
		if flag.Type == typePath {
			directive = cobra.ShellCompDirectiveDefault
		}
		choices := slices.Clone(flag.Choices)
		_ = cmd.RegisterFlagCompletionFunc(flag.Name, func(*cobra.Command, []string, string) ([]cobra.Completion,
			cobra.ShellCompDirective) {
			return choices, directive
		})
	}
	return cmd
}

func defaultText(value any) string {
	if items, isList := value.([]any); isList {
		texts := make([]string, 0, len(items))
		for _, item := range items {
			texts = append(texts, fmt.Sprint(item))
		}
		return "[" + strings.Join(texts, ",") + "]"
	}
	return fmt.Sprint(value)
}

func (c *Command) use() string {
	words := []string{c.Path[len(c.Path)-1]}
	for _, arg := range c.Args {
		var text string
		if arg.Group != nil {
			slots := make([]string, 0, len(arg.Group))
			for _, slot := range arg.Group {
				slots = append(slots, "<"+slot.Name+">")
			}
			text = strings.Join(slots, " ")
			if arg.variable() {
				text = "(" + text + ")"
			}
		} else {
			text = "<" + arg.Name + ">"
		}
		if arg.Multiple {
			text += "..."
		}
		if arg.Optional {
			text = "[" + text + "]"
		}
		words = append(words, text)
	}
	return strings.Join(words, " ")
}

func (c *Command) long() string {
	if len(c.Args) == 0 {
		return c.Long
	}
	text := c.Long + "\n\nArguments:"
	for _, arg := range c.Args {
		text += fmt.Sprintf("\n  <%s>  %s", arg.Name, arg.Usage)
		for _, slot := range arg.Group {
			text += fmt.Sprintf("\n    <%s>  %s", slot.Name, slot.Usage)
		}
	}
	return text
}

// split gives every declared argument its words: the elements before the variable one take theirs from the left,
// the ones after it from the right, and the variable one takes the middle, divided by its width.
func (c *Command) split(args []string) ([][]string, error) {
	fixed, variable := 0, -1
	for i, arg := range c.Args {
		if arg.variable() {
			variable = i
			continue
		}
		fixed += arg.width()
	}
	words := make([][]string, len(c.Args))
	if variable < 0 {
		if len(args) != fixed {
			return nil, &UsageError{Message: fmt.Sprintf("%s takes %d arguments, and it was given %d", c.name0(),
				fixed, len(args))}
		}
	} else {
		middle := len(args) - fixed
		arg := c.Args[variable]
		width := arg.width()
		least := 1
		if arg.Optional {
			least = 0
		}
		switch {
		case middle < least*width:
			return nil, &UsageError{Message: fmt.Sprintf("%s takes at least %d arguments, and it was given %d",
				c.name0(), fixed+least*width, len(args))}
		case middle%width != 0:
			return nil, &UsageError{Message: fmt.Sprintf("<%s> takes its arguments in groups of %d, and it was "+
				"given %d", arg.Name, width, middle)}
		case !arg.Multiple && middle > width:
			return nil, &UsageError{Message: fmt.Sprintf("%s takes at most %d arguments, and it was given %d",
				c.name0(), fixed+width, len(args))}
		}
	}
	at := 0
	for i, arg := range c.Args {
		taken := arg.width()
		if i == variable {
			taken = len(args) - fixed
		}
		words[i] = args[at : at+taken]
		at += taken
	}
	return words, nil
}

func (c *Command) name0() string {
	return quote(strings.Join(c.Path, " "))
}

func (c *Command) input(args []string, values []*flagText) (Input, error) {
	words, err := c.split(args)
	if err != nil {
		return Input{}, err
	}
	in := Input{Args: make([]any, 0, len(c.Args))}
	for i, arg := range c.Args {
		given := words[i]
		var items []any
		for len(given) > 0 {
			item, err := c.element(arg, given[:arg.width()])
			if err != nil {
				return Input{}, err
			}
			items = append(items, item)
			given = given[arg.width():]
		}
		switch {
		case len(items) == 0:
			in.Args = append(in.Args, Undefined{})
		case arg.Multiple:
			in.Args = append(in.Args, items)
		default:
			in.Args = append(in.Args, items[0])
		}
	}
	var keys []string
	var flags []any
	for _, value := range values {
		if held, given := flagValue(c.types, value.flag, value.parsed); given {
			keys = append(keys, value.flag.Name)
			flags = append(flags, held)
		}
	}
	in.Flags = NewObject(keys, flags)
	return in, nil
}

func (c *Command) element(arg Arg, words []string) (any, error) {
	if arg.Group == nil {
		return c.parseArg(arg.Name, arg.Type, words[0])
	}
	keys := make([]string, 0, len(arg.Group))
	values := make([]any, 0, len(arg.Group))
	for i, slot := range arg.Group {
		value, err := c.parseArg(slot.Name, slot.Type, words[i])
		if err != nil {
			return nil, err
		}
		keys = append(keys, slot.Name)
		values = append(values, value)
	}
	return NewObject(keys, values), nil
}

func (c *Command) parseArg(name, kind, text string) (any, error) {
	value, err := parse(c.types, kind, nil, text)
	if err != nil {
		return nil, &UsageError{Message: fmt.Sprintf("invalid argument %s for <%s>: %v", quote(text), name, err)}
	}
	return argValue(value), nil
}

// pathAt tells whether the argument at index may be a path, for the shell to offer file names. Past the elements
// before the variable one, the word is either a slot of the variable element or one of the elements after it.
func (c *Command) pathAt(index int) bool {
	var before, after []string
	variable := -1
	for i, arg := range c.Args {
		switch {
		case arg.variable():
			variable = i
		case variable < 0:
			before = append(before, kinds(arg)...)
		default:
			after = append(after, kinds(arg)...)
		}
	}
	if index < len(before) {
		return before[index] == typePath
	}
	if variable < 0 {
		return false
	}
	slots := kinds(c.Args[variable])
	offset := index - len(before)
	if slots[offset%len(slots)] == typePath && (c.Args[variable].Multiple || offset < len(slots)) {
		return true
	}
	for taken := 0; taken <= offset; taken += len(slots) {
		if at := offset - taken; at < len(after) && after[at] == typePath {
			return true
		}
		if !c.Args[variable].Multiple && taken > 0 {
			break
		}
	}
	return false
}

func kinds(arg Arg) []string {
	if arg.Group == nil {
		return []string{arg.Type}
	}
	kinds := make([]string, 0, len(arg.Group))
	for _, slot := range arg.Group {
		kinds = append(kinds, slot.Type)
	}
	return kinds
}

// flagText is the pflag value of a declared flag: it parses every value of the call by the type of the flag.
type flagText struct {
	types  map[string]Type
	flag   Flag
	texts  []string
	parsed []any
}

var errRepeated = errors.New("the flag is given more than once")

func (v *flagText) Set(text string) error {
	if len(v.texts) > 0 && !v.flag.Multiple {
		return errRepeated
	}
	parsed, err := parse(v.types, v.flag.Type, v.flag.Choices, text)
	if err != nil {
		return err
	}
	v.texts = append(v.texts, text)
	v.parsed = append(v.parsed, parsed)
	return nil
}

func (v *flagText) String() string {
	return strings.Join(v.texts, ",")
}

func (v *flagText) Type() string {
	if v.flag.Type == typeBool {
		return "bool"
	}
	return v.flag.Type
}

var _ pflag.Value = (*flagText)(nil)
