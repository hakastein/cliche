package cliche

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	typeString   = "string"
	typePath     = "path"
	typeInt      = "int"
	typeBool     = "bool"
	typePair     = "pair"
	typeDuration = "duration"
)

// Type is a type of the app for arguments, slots and flags beyond the base ones. A base type name is never an app
// type.
type Type struct {
	// Usage is the fixed help of a value of the type; a declaration writes none.
	Usage string
	// Default makes default required of a flag of the type.
	Default bool
	// Parse reads every value of the call; nil keeps the text as it is.
	Parse func(text string) (any, error)
	// Value is what command gets for a flag of the type from its default and the values given; nil leaves the key
	// out. Value nil stands for the last value given, else the default.
	Value func(def any, given []any) any
}

// parse reads one value of the call by its type; choices bound a string.
func parse(types map[string]Type, kind string, choices []string, text string) (any, error) {
	switch kind {
	case typeString:
		if len(choices) > 0 && !slices.Contains(choices, text) {
			return nil, errors.New("it is none of " + strings.Join(choices, ", "))
		}
		return text, nil
	case typePath:
		return text, nil
	case typeInt:
		number, err := strconv.ParseInt(text, 0, 32)
		if err != nil {
			return nil, errors.New("it is no whole number of 32 bits")
		}
		return number, nil
	case typeBool:
		set, err := strconv.ParseBool(text)
		if err != nil {
			return nil, errors.New("it is neither true nor false")
		}
		return set, nil
	case typePair:
		name, value, split := strings.Cut(text, "=")
		switch {
		case !split:
			return nil, errors.New("it holds no =: a pair is written as its name, an = and the value, as in Type=Task")
		case name == "":
			return nil, errors.New("it names nothing before the =")
		}
		return pair{name: name, value: value}, nil
	case typeDuration:
		return durationMinutes(text)
	}
	if app := types[kind]; app.Parse != nil {
		return app.Parse(text)
	}
	return text, nil
}

type pair struct {
	name  string
	value string
}

func (p pair) object() *Object {
	return NewObject([]string{p.name}, []any{p.value})
}

// pairsObject gathers pairs into one object; a name given more than once holds the array of its values.
func pairsObject(pairs []pair) *Object {
	var names []string
	held := map[string][]any{}
	for _, p := range pairs {
		if _, seen := held[p.name]; !seen {
			names = append(names, p.name)
		}
		held[p.name] = append(held[p.name], p.value)
	}
	values := make([]any, 0, len(names))
	for _, name := range names {
		if len(held[name]) == 1 {
			values = append(values, held[name][0])
			continue
		}
		values = append(values, held[name])
	}
	return NewObject(names, values)
}

// flagValue is what command gets for a flag; false leaves the key out.
func flagValue(types map[string]Type, flag Flag, given []any) (any, bool) {
	if app, isApp := types[flag.Type]; isApp && !slices.Contains(flagTypes, flag.Type) {
		if app.Value != nil {
			value := app.Value(flag.Default, given)
			return value, value != nil
		}
		if len(given) > 0 {
			return given[len(given)-1], true
		}
		return flag.Default, flag.Default != nil
	}
	if len(given) == 0 {
		return flag.Default, flag.Default != nil
	}
	switch {
	case flag.Type == typePair && flag.Multiple:
		pairs := make([]pair, 0, len(given))
		for _, value := range given {
			pairs = append(pairs, value.(pair))
		}
		return pairsObject(pairs), true
	case flag.Type == typePair:
		return given[0].(pair).object(), true
	case flag.Multiple:
		return slices.Clone(given), true
	}
	return given[len(given)-1], true
}

// argValue turns a parsed value of an argument or a slot into a value of the bridge.
func argValue(value any) any {
	if p, isPair := value.(pair); isPair {
		return p.object()
	}
	return value
}

// A duration is whole minutes that time.Duration holds.
const longestDuration = math.MaxInt64 / int64(time.Minute)

func durationMinutes(text string) (int64, error) {
	minutes, read := periodMinutes(text)
	switch {
	case !read:
		return 0, errors.New("it is no ISO 8601 period of hours and minutes, as in PT1H30M, PT90M or PT0M: neither a " +
			"day nor a week is a fixed count of minutes, and a second is no part of it")
	case minutes > longestDuration:
		return 0, fmt.Errorf("it is longer than %d minutes", longestDuration)
	}
	return minutes, nil
}

func periodMinutes(text string) (int64, bool) {
	rest, isPeriod := strings.CutPrefix(text, "PT")
	if !isPeriod {
		return 0, false
	}
	hours, rest, hoursGiven, read := countBefore(rest, 'H')
	if !read {
		return 0, false
	}
	minutes, rest, minutesGiven, read := countBefore(rest, 'M')
	if !read || rest != "" || (!hoursGiven && !minutesGiven) {
		return 0, false
	}
	return min(hours*60, pastLongest) + minutes, true
}

const pastLongest = longestDuration + 1

func countBefore(text string, mark byte) (count int64, rest string, given, read bool) {
	before, after, marked := strings.Cut(text, string(mark))
	if !marked {
		return 0, text, false, true
	}
	if before == "" {
		return 0, "", false, false
	}
	for _, digit := range []byte(before) {
		if digit < '0' || digit > '9' {
			return 0, "", false, false
		}
		count = min(count*10+int64(digit-'0'), pastLongest)
	}
	return count, after, true, true
}
