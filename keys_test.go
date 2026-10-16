package cliche_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

// collector is what an app of project collectors reads from its key: no type of cliche.
type collector struct {
	scope string
	facts []string
}

// readCollector is the reader of that app: an object of a string scope and an array of string facts.
func readCollector(value cliche.Literal) (any, error) {
	held, isObject := value.Value.(*cliche.LiteralObject)
	if !isObject {
		return nil, errors.New("a collector is an object")
	}
	scope, given := held.Values["scope"]
	if !given {
		return nil, value.Errorf("a collector has a scope")
	}
	read := collector{facts: []string{}}
	if read.scope, given = scope.Value.(string); !given {
		return nil, scope.Errorf("a scope is a string")
	}
	facts, _ := held.Values["facts"].Value.([]cliche.Literal)
	for _, fact := range facts {
		text, isString := fact.Value.(string)
		if !isString {
			return nil, fact.Errorf("a fact is a string")
		}
		read.facts = append(read.facts, text)
	}
	return read, nil
}

func readOwner(value cliche.Literal) (any, error) {
	owner, isString := value.Value.(string)
	if !isString {
		return nil, value.Errorf("an owner is a string")
	}
	return owner, nil
}

// The two apps register different keys: one requires collector, the other may take owner.
var (
	collectorKeys = map[string]cliche.Key{"collector": {Required: true, Read: readCollector}}
	ownerKeys     = map[string]cliche.Key{"owner": {Read: readOwner}}
)

// keyed is a command script whose module throws as soon as it runs; entries are the lines of the declaration after
// short and long, from line 5.
func keyed(entries ...string) map[string]string {
	head := []string{`throw new Error("the module ran");`, `exports.definition = {`, `  short: "Collect",`,
		`  long: "Collect routes.",`}
	return map[string]string{"run.js": lines(append(append(head, entries...), `};`, echoing)...)}
}

func withKeys(files map[string]string, keys map[string]cliche.Key) cliche.Config {
	config := scripts(files)
	config.Keys = keys
	return config
}

// scannedKeys is what Scan gives the app of the only command, before any call.
func scannedKeys(t *testing.T, config cliche.Config) map[string]any {
	t.Helper()
	catalog := cliche.Merge(config.Scan())
	require.Empty(t, catalog.Broken)
	require.Len(t, catalog.Commands, 1)
	return catalog.Commands[0].Keys
}

// printed is a call and what it printed.
type printed struct {
	outcome
	out string
}

func executePrinted(t *testing.T, config cliche.Config, argv ...string) printed {
	t.Helper()
	var got printed
	root := app(config, cliche.Env{}, &got.outcome)
	out := new(bytes.Buffer)
	root.SetArgs(argv)
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	got.err = root.Execute()
	got.out = out.String()
	return got
}

func TestAppReadsItsOwnKeysWithoutRunningTheModule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config cliche.Config
		want   map[string]any
	}{
		{
			name: "a key of one app",
			config: withKeys(keyed(`  collector: { scope: "snapshot", facts: ["php.symbols", "js." + "routes"] },`),
				collectorKeys),
			want: map[string]any{"collector": collector{scope: "snapshot", facts: []string{"php.symbols", "js.routes"}}},
		},
		{
			name:   "a key of the other app",
			config: withKeys(keyed(`  owner: "backend",`), ownerKeys),
			want:   map[string]any{"owner": "backend"},
		},
		{
			name:   "an optional key not given",
			config: withKeys(keyed(), ownerKeys),
			want:   map[string]any{},
		},
		{
			name:   "an app with no keys",
			config: scripts(keyed()),
			want:   nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, scannedKeys(t, tc.config))
		})
	}
}

func TestKeyWithoutReadKeepsTheLiteralWithItsPlaces(t *testing.T) {
	t.Parallel()
	config := withKeys(keyed(`  meta: { tags: ["a", "b" + "c"], n: 2, on: true, none: null },`),
		map[string]cliche.Key{"meta": {}})
	want := cliche.Literal{Line: 5, Column: 9, Value: &cliche.LiteralObject{
		Keys: []string{"tags", "n", "on", "none"},
		Values: map[string]cliche.Literal{
			"tags": {Line: 5, Column: 17, Value: []cliche.Literal{{Line: 5, Column: 18, Value: "a"},
				{Line: 5, Column: 23, Value: "bc"}}},
			"n":    {Line: 5, Column: 38, Value: float64(2)},
			"on":   {Line: 5, Column: 45, Value: true},
			"none": {Line: 5, Column: 57, Value: nil},
		},
	}}

	assert.Equal(t, map[string]any{"meta": want}, scannedKeys(t, config))
}

func TestDeclarationIsUnreadableAtThePlaceOfAKeyRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		config       cliche.Config
		line, column int
	}{
		{name: "a key of the other app", config: withKeys(keyed(`  owner: "backend",`), collectorKeys),
			line: 2, column: 22},
		{name: "a key of the one app", config: withKeys(keyed(`  owner: "backend",`,
			`  collector: { scope: "s", facts: [] },`), ownerKeys), line: 2, column: 22},
		{name: "a key of neither app", config: withKeys(keyed(`  collector: { scope: "s", facts: [] },`,
			`  colector: 1,`), collectorKeys), line: 2, column: 22},
		{name: "a required key not given", config: withKeys(keyed(), collectorKeys), line: 2, column: 22},
		{name: "a value refused by a plain error", config: withKeys(keyed(`  collector: "routes",`), collectorKeys),
			line: 5, column: 14},
		{name: "a value refused at itself", config: withKeys(keyed(`  collector: { facts: [] },`), collectorKeys),
			line: 5, column: 14},
		{name: "a field refused", config: withKeys(keyed(`  collector: {`, `    scope: 1,`, `    facts: [],`, `  },`),
			collectorKeys), line: 6, column: 12},
		{name: "an item of a nested array refused", config: withKeys(keyed(`  collector: {`, `    scope: "snapshot",`,
			`    facts: ["php.symbols", 7],`, `  },`), collectorKeys), line: 7, column: 28},
		{name: "a name", config: withKeys(keyed(`  collector: { scope: SCOPE, facts: [] },`), collectorKeys),
			line: 5, column: 23},
		{name: "a call", config: withKeys(keyed(`  collector: String("x"),`), collectorKeys), line: 5, column: 14},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := execute(t, tc.config, cliche.Env{}, "run")

			failed := requireAs[*cliche.ScriptError](t, got.err)
			assert.Equal(t, []any{"test:run.js", tc.line, tc.column}, []any{failed.File, failed.Line, failed.Column})
			assert.False(t, got.ran)
		})
	}
}

func TestKeyOfTheAppThatClicheReadsItselfIsRefused(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"short", "long", "args", "flags"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := withKeys(keyed(), map[string]cliche.Key{name: {}})

			assert.Panics(t, func() { config.Scan() })
		})
	}
}

func TestKeyOfTheAppLeavesTheCallTheHelpAndTheArgumentsAsTheyAre(t *testing.T) {
	t.Parallel()
	const body = `short: "Check", long: "Check.", args: [{ name: "word", type: "string", usage: "a word" }], ` +
		`flags: [{ name: "mode", type: "string", usage: "a mode", choices: ["fast", "slow"], default: "fast" }]`
	plain := scripts(echoScript(`{ ` + body + ` }`))
	extended := withKeys(echoScript(`{ `+body+`, collector: { scope: "snapshot", facts: [] } }`), collectorKeys)
	tests := []struct {
		name string
		argv []string
	}{
		{name: "a call", argv: []string{"run", "a", "--mode", "slow"}},
		{name: "the help", argv: []string{"run", "--help"}},
		{name: "a call the declaration does not take", argv: []string{"run", "a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, executePrinted(t, plain, tc.argv...), executePrinted(t, extended, tc.argv...))
		})
	}
}
