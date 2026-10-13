package cliche_test

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

// echoing gives back the arguments command got, an undefined one as "<undefined>", and the flags.
const echoing = `exports.command = (...args) => args.map((arg) => arg === undefined ? "<undefined>" : arg);`

func echoScript(definition string) map[string]string {
	return map[string]string{"run.js": lines(`exports.definition = `+definition+`;`, echoing)}
}

func TestArgumentsReachCommandInTheirDeclaredLayout(t *testing.T) {
	t.Parallel()
	const def = `{ short: "Def", long: "Def.", args: [` +
		`{ name: "at", usage: "places", multiple: true, group: [` +
		`{ name: "position", type: "string", usage: "path:line" }, { name: "expression", type: "string", usage: "an expression" }] },` +
		`{ name: "out", type: "path", usage: "where" }] }`
	tests := []struct {
		name       string
		definition string
		argv       []string
		want       []any
	}{
		{
			name:       "a multiple group divided by its width before a fixed suffix",
			definition: def,
			argv:       []string{"a.go:1", "x", "b.go:2", "y", "out.txt"},
			want: []any{[]any{object("position", "a.go:1", "expression", "x"),
				object("position", "b.go:2", "expression", "y")}, "out.txt", object()},
		},
		{
			name:       "a multiple group given once",
			definition: def,
			argv:       []string{"a.go:1", "x", "out.txt"},
			want:       []any{[]any{object("position", "a.go:1", "expression", "x")}, "out.txt", object()},
		},
		{
			name: "an optional argument not given",
			definition: `{ short: "Stop", long: "Stop.", args: [{ name: "tree", type: "string", usage: "a tree", ` +
				`optional: true }] }`,
			argv: nil,
			want: []any{"<undefined>", object()},
		},
		{
			name: "an optional argument given",
			definition: `{ short: "Stop", long: "Stop.", args: [{ name: "tree", type: "string", usage: "a tree", ` +
				`optional: true }] }`,
			argv: []string{"main"},
			want: []any{"main", object()},
		},
		{
			name: "a fixed prefix, a multiple middle and a fixed suffix",
			definition: `{ short: "Pick", long: "Pick.", args: [{ name: "first", type: "int", usage: "n" }, ` +
				`{ name: "rest", type: "string", usage: "s", multiple: true }, ` +
				`{ name: "last", type: "duration", usage: "d" }] }`,
			argv: []string{"7", "a", "b", "c", "PT1H30M"},
			want: []any{int64(7), []any{"a", "b", "c"}, int64(90), object()},
		},
		{
			name: "an optional group not given between fixed arguments",
			definition: `{ short: "Pick", long: "Pick.", args: [{ name: "first", type: "string", usage: "s" }, ` +
				`{ name: "pair", usage: "p", optional: true, group: [{ name: "a", type: "int", usage: "a" }, ` +
				`{ name: "b", type: "int", usage: "b" }] }, { name: "last", type: "string", usage: "s" }] }`,
			argv: []string{"x", "y"},
			want: []any{"x", "<undefined>", "y", object()},
		},
		{
			name:       "a pair argument",
			definition: `{ short: "Set", long: "Set.", args: [{ name: "field", type: "pair", usage: "a field" }] }`,
			argv:       []string{"Type=Task"},
			want:       []any{object("Type", "Task"), object()},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := execute(t, scripts(echoScript(tc.definition)), cliche.Env{}, append([]string{"run"}, tc.argv...)...)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

const flagged = `{ short: "Check", long: "Check.", flags: [` +
	`{ name: "mode", type: "string", usage: "a mode", choices: ["fast", "slow"], default: "fast" },` +
	`{ name: "count", type: "int", usage: "how many" },` +
	`{ name: "verbose", type: "bool", usage: "say more" },` +
	`{ name: "tag", type: "string", multiple: true, usage: "a tag", choices: ["a", "b", "c"] },` +
	`{ name: "set", type: "pair", multiple: true, usage: "a pair" },` +
	`{ name: "one", type: "pair", usage: "one pair" },` +
	`{ name: "spent", type: "duration", usage: "how long" },` +
	`{ name: "file", type: "path", usage: "a file" },` +
	`{ name: "labels", type: "string", multiple: true, usage: "labels", default: ["x", "y"] }] }`

func TestFlagsReachCommandByTheirTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		argv []string
		want *cliche.Object
	}{
		{
			name: "only the defaults",
			argv: nil,
			want: object("mode", "fast", "labels", []any{"x", "y"}),
		},
		{
			name: "every flag",
			argv: []string{"--mode", "slow", "--count", "-3", "--verbose", "--tag", "b", "--tag", "a", "--set", "A=1",
				"--set", "B=x=y", "--set", "A=2", "--one", "K=v", "--spent", "PT2H", "--file", "f.txt", "--labels", "z"},
			want: object("mode", "slow", "count", int64(-3), "verbose", true, "tag", []any{"b", "a"},
				"set", object("A", []any{"1", "2"}, "B", "x=y"), "one", object("K", "v"), "spent", int64(120),
				"file", "f.txt", "labels", []any{"z"}),
		},
		{
			name: "a bool set false",
			argv: []string{"--verbose=false"},
			want: object("mode", "fast", "verbose", false, "labels", []any{"x", "y"}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := execute(t, scripts(echoScript(flagged)), cliche.Env{}, append([]string{"run"}, tc.argv...)...)

			require.NoError(t, got.err)
			assert.Equal(t, []any{tc.want}, got.value)
		})
	}
}

func TestCallIsRefusedBeforeTheScriptRuns(t *testing.T) {
	t.Parallel()
	const def = `{ short: "Def", long: "Def.", args: [` +
		`{ name: "at", usage: "places", multiple: true, group: [` +
		`{ name: "line", type: "int", usage: "a line" }, { name: "expression", type: "string", usage: "e" }] },` +
		`{ name: "out", type: "path", usage: "where" }], flags: ` + `[` +
		`{ name: "mode", type: "string", usage: "a mode", choices: ["fast", "slow"] },` +
		`{ name: "count", type: "int", usage: "how many" },` +
		`{ name: "verbose", type: "bool", usage: "say more" },` +
		`{ name: "tag", type: "string", multiple: true, usage: "a tag", choices: ["a", "b"] },` +
		`{ name: "set", type: "pair", usage: "a pair" },` +
		`{ name: "spent", type: "duration", usage: "how long" }] }`
	valid := []string{"run", "1", "x", "out"}
	tests := []struct {
		name string
		argv []string
	}{
		{name: "an unknown flag", argv: append(valid, "--nope", "1")},
		{name: "a value outside the choices", argv: append(valid, "--mode", "medium")},
		{name: "a value outside the choices of a multiple flag", argv: append(valid, "--tag", "c")},
		{name: "a flag with no value", argv: append(valid, "--mode")},
		{name: "a flag given twice", argv: append(valid, "--mode", "fast", "--mode", "slow")},
		{name: "a bool given twice", argv: append(valid, "--verbose", "--verbose")},
		{name: "an int wider than 32 bits", argv: append(valid, "--count", "3000000000")},
		{name: "an int that is no number", argv: append(valid, "--count", "many")},
		{name: "a pair with no =", argv: append(valid, "--set", "A")},
		{name: "a pair with no name", argv: append(valid, "--set", "=1")},
		{name: "a duration of days", argv: append(valid, "--spent", "P1D")},
		{name: "too few arguments", argv: []string{"run", "1", "x"}},
		{name: "a group cut short", argv: []string{"run", "1", "x", "2", "out"}},
		{name: "a slot its type does not read", argv: []string{"run", "one", "x", "out"}},
		{name: "no arguments", argv: []string{"run"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := &recorder{}
			files := map[string]string{"run.js": lines(`const api = require("api");`,
				`exports.definition = `+def+`;`, `exports.command = () => api.touch();`)}
			env := cliche.Env{Modules: map[string]any{"api": object("touch", calls.Func("touch", answering(nil)))}}

			got := execute(t, scripts(files), env, tc.argv...)

			requireAs[*cliche.UsageError](t, got.err)
			assert.False(t, got.ran)
			assert.Empty(t, calls.recorded())
		})
	}
}

func TestOptionalArgumentRefusesASecondValue(t *testing.T) {
	t.Parallel()
	files := echoScript(`{ short: "Stop", long: "Stop.", args: [{ name: "tree", type: "string", usage: "a tree", ` +
		`optional: true }] }`)

	got := execute(t, scripts(files), cliche.Env{}, "run", "a", "b")

	requireAs[*cliche.UsageError](t, got.err)
	assert.False(t, got.ran)
}

// fields is the fields type of ytrack, as an app registers it: + adds to the default, empty is the default.
var fields = cliche.Type{
	Usage:   "fields `expression`; +expr adds to the default",
	Default: true,
	Value: func(def any, given []any) any {
		if len(given) == 0 {
			return def
		}
		text := strings.TrimSpace(given[len(given)-1].(string))
		added, adds := strings.CutPrefix(text, "+")
		switch {
		case text == "" || adds && strings.TrimSpace(added) == "":
			return def
		case adds:
			return def.(string) + "," + strings.TrimSpace(added)
		}
		return text
	},
}

var upper = cliche.Type{
	Usage: "a `WORD`",
	Parse: func(text string) (any, error) {
		if text == "" {
			return nil, assert.AnError
		}
		return strings.ToUpper(text), nil
	},
}

func TestAppTypeParsesAndGivesTheValueOfAFlag(t *testing.T) {
	t.Parallel()
	files := echoScript(`{ short: "Show", long: "Show.", args: [{ name: "word", type: "upper" }], ` +
		`flags: [{ name: "fields", type: "fields", default: "id,summary" }] }`)
	config := scripts(files)
	config.Types = map[string]cliche.Type{"fields": fields, "upper": upper}
	tests := []struct {
		name string
		argv []string
		want []any
	}{
		{name: "the flag left out", argv: []string{"x"}, want: []any{"X", object("fields", "id,summary")}},
		{name: "an expression added to the default", argv: []string{"x", "--fields", "+key"},
			want: []any{"X", object("fields", "id,summary,key")}},
		{name: "an empty expression", argv: []string{"x", "--fields", ""},
			want: []any{"X", object("fields", "id,summary")}},
		{name: "an expression of its own", argv: []string{"x", "--fields", "key"},
			want: []any{"X", object("fields", "key")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := execute(t, config, cliche.Env{}, append([]string{"run"}, tc.argv...)...)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestAppTypeRefusesWhatItsParseRefuses(t *testing.T) {
	t.Parallel()
	config := scripts(echoScript(`{ short: "Show", long: "Show.", args: [{ name: "word", type: "upper" }] }`))
	config.Types = map[string]cliche.Type{"upper": upper}

	got := execute(t, config, cliche.Env{}, "run", "")

	requireAs[*cliche.UsageError](t, got.err)
	assert.False(t, got.ran)
}

func TestUnreadableDeclarationIsAScriptErrorWithItsPlace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		definition string
		line       int
	}{
		{name: "an unknown key", definition: `{ short: "S", long: "L", hidden: true }`},
		{name: "an unknown key of a flag", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "string", usage: "u", deep: 1 }] }`},
		{name: "an unknown key of a slot", definition: `{ short: "S", long: "L", args: [{ name: "g", usage: "u", group: [{ name: "a", type: "string", usage: "u", x: 1 }] }] }`},
		{name: "a call", definition: `{ short: String("S"), long: "L" }`},
		{name: "a name", definition: `{ short: SHORT, long: "L" }`},
		{name: "no long", definition: `{ short: "S" }`},
		{name: "a short of two lines", definition: `{ short: "S\nT", long: "L" }`},
		{name: "a short too long", definition: `{ short: "` + strings.Repeat("s", 61) + `", long: "L" }`},
		{name: "two variable arguments", definition: `{ short: "S", long: "L", args: [{ name: "a", type: "string", usage: "u", optional: true }, { name: "b", type: "string", usage: "u", multiple: true }] }`},
		{name: "an argument named twice", definition: `{ short: "S", long: "L", args: [{ name: "a", type: "string", usage: "u" }, { name: "a", type: "string", usage: "u" }] }`},
		{name: "a slot named twice", definition: `{ short: "S", long: "L", args: [{ name: "g", usage: "u", group: [{ name: "a", type: "string", usage: "u" }, { name: "a", type: "string", usage: "u" }] }] }`},
		{name: "an empty group", definition: `{ short: "S", long: "L", args: [{ name: "g", usage: "u", group: [] }] }`},
		{name: "a group with a type", definition: `{ short: "S", long: "L", args: [{ name: "g", type: "string", usage: "u", group: [{ name: "a", type: "string", usage: "u" }] }] }`},
		{name: "a bool argument", definition: `{ short: "S", long: "L", args: [{ name: "a", type: "bool", usage: "u" }] }`},
		{name: "an unknown type", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "date", usage: "u" }] }`},
		{name: "a flag named help", definition: `{ short: "S", long: "L", flags: [{ name: "help", type: "bool", usage: "u" }] }`},
		{name: "a name starting with a dash", definition: `{ short: "S", long: "L", flags: [{ name: "-a", type: "bool", usage: "u" }] }`},
		{name: "a multiple int flag", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "int", usage: "u", multiple: true }] }`},
		{name: "choices of an int flag", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "int", usage: "u", choices: ["1"] }] }`},
		{name: "a default of a pair flag", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "pair", usage: "u", default: "A=1" }] }`},
		{name: "a default of a duration flag", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "duration", usage: "u", default: "PT1M" }] }`},
		{name: "a default outside the choices", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "string", usage: "u", choices: ["x"], default: "y" }] }`},
		{name: "a default of another type", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "int", usage: "u", default: "1" }] }`},
		{name: "an app type with no default it takes", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "fields" }] }`},
		{name: "an app type with a usage of its own", definition: `{ short: "S", long: "L", flags: [{ name: "a", type: "fields", usage: "u", default: "id" }] }`},
		{name: "a syntax error", definition: `{ short: "S", long: "L", }}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := scripts(map[string]string{"run.js": lines(`// a command`, `exports.definition = `+tc.definition+`;`,
				`exports.command = () => ({});`)})
			config.Types = map[string]cliche.Type{"fields": fields}

			got := execute(t, config, cliche.Env{}, "run")

			failed := requireAs[*cliche.ScriptError](t, got.err)
			assert.Equal(t, "test:run.js", failed.File)
			assert.Equal(t, 2, failed.Line)
			assert.False(t, got.ran)
		})
	}
}

// throwing is a command script whose module throws as soon as it runs.
var throwing = map[string]string{"run.js": lines(
	`throw new Error("the module ran");`,
	`exports.definition = { short: "Run", long: "Run it.", args: [{ name: "at", type: "path", usage: "a file" }], `+
		`flags: [{ name: "mode", type: "string", usage: "a mode", choices: ["fast", "slow"] }] };`,
	`exports.command = () => ({});`,
)}

func TestHelpIsReadFromTheDeclarationWithoutRunningTheModule(t *testing.T) {
	t.Parallel()
	got := execute(t, scripts(throwing), cliche.Env{}, "run", "--help")

	require.NoError(t, got.err)
	assert.False(t, got.ran)
}

func TestCompletionIsReadFromTheDeclarationWithoutRunningTheModule(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"run.js": throwing["run.js"],
		"def.js": lines(`throw new Error("the module ran");`, `exports.definition = { short: "Def", long: "Def.", args: [`+
			`{ name: "at", usage: "places", multiple: true, group: [{ name: "name", type: "string", usage: "n" }, `+
			`{ name: "file", type: "path", usage: "f" }] }, { name: "word", type: "string", usage: "w" }], flags: [`+
			`{ name: "out", type: "path", usage: "a file" }, { name: "tag", type: "string", multiple: true, usage: "t", `+
			`choices: ["a", "b"] }] };`),
	}
	tests := []struct {
		name string
		argv []string
		want completion
	}{
		{name: "the commands", argv: []string{"d"}, want: completion{names: []string{"def"},
			directive: cobra.ShellCompDirectiveNoFileComp}},
		{name: "an argument that is a path", argv: []string{"run", ""}, want: completion{names: []string{},
			directive: cobra.ShellCompDirectiveDefault}},
		{name: "past the arguments", argv: []string{"run", "a", ""}, want: completion{names: []string{},
			directive: cobra.ShellCompDirectiveNoFileComp}},
		{name: "the choices of a flag", argv: []string{"run", "--mode", ""}, want: completion{
			names: []string{"fast", "slow"}, directive: cobra.ShellCompDirectiveNoFileComp}},
		{name: "the choices of a multiple flag", argv: []string{"def", "--tag", "a", "--tag", ""}, want: completion{
			names: []string{"a", "b"}, directive: cobra.ShellCompDirectiveNoFileComp}},
		{name: "a flag that is a path", argv: []string{"def", "--out", ""}, want: completion{names: []string{},
			directive: cobra.ShellCompDirectiveDefault}},
		{name: "a slot that is no path", argv: []string{"def", ""}, want: completion{names: []string{},
			directive: cobra.ShellCompDirectiveNoFileComp}},
		{name: "a slot that is a path", argv: []string{"def", "n", ""}, want: completion{names: []string{},
			directive: cobra.ShellCompDirectiveDefault}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, complete(t, scripts(files), tc.argv...))
		})
	}
}
