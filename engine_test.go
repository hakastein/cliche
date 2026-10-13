package cliche_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

func TestRequireTakesLibraryModulesOfItsOwnSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		argv  []string
		want  any
	}{
		{
			name: "a library module beside the command",
			files: map[string]string{"lib.js": "exports.word = () => \"lib\";\n",
				"run.js": lines(`const lib = require("./lib");`, `exports.definition = { short: "Run", long: "Run it." };`,
					`exports.command = () => lib.word();`)},
			argv: []string{"run"},
			want: "lib",
		},
		{
			name: "a library module above the command, named with .js",
			files: map[string]string{"lib/text.js": "exports.word = () => \"text\";\n",
				"docs/map.js": lines(`const text = require("../lib/text.js");`,
					`exports.definition = { short: "Map", long: "Map it." };`, `exports.command = () => text.word();`)},
			argv: []string{"docs", "map"},
			want: "text",
		},
		{
			name: "a library module required twice is one module",
			files: map[string]string{"lib.js": "exports.count = 0; exports.add = () => ++exports.count;\n",
				"other.js": "exports.lib = require(\"./lib\");\n",
				"run.js": lines(`const lib = require("./lib"); const other = require("./other");`,
					`exports.definition = { short: "Run", long: "Run it." };`,
					`exports.command = () => { lib.add(); other.lib.add(); return lib.count; };`)},
			argv: []string{"run"},
			want: int64(2),
		},
		{
			name: "a module with a shebang",
			files: map[string]string{"run.js": lines(`#!/usr/bin/env node`,
				`exports.definition = { short: "Run", long: "Run it." };`, `exports.command = () => "ran";`)},
			argv: []string{"run"},
			want: "ran",
		},
		{
			name: "a module of the app",
			files: map[string]string{"run.js": lines(`const api = require("api");`,
				`exports.definition = { short: "Run", long: "Run it." };`, `exports.command = () => api.name;`)},
			argv: []string{"run"},
			want: "api",
		},
		{
			name: "the buffer module",
			files: map[string]string{"run.js": lines(`const { Buffer: B } = require("node:buffer");`,
				`exports.definition = { short: "Run", long: "Run it." };`,
				`exports.command = () => B === Buffer && Buffer.isBuffer(B.from("x")) && !Buffer.isBuffer(new Uint8Array(1));`)},
			argv: []string{"run"},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := cliche.Env{Modules: map[string]any{"api": object("name", "api")}, Node: cliche.NodeBuffer}

			got := execute(t, scripts(tc.files), env, tc.argv...)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestRequireRefusesWhatIsNoLibraryModuleOfItsSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		wanted string
	}{
		{name: "a command script", wanted: `"./other"`},
		{name: "a path out of the source", wanted: `"../outside"`},
		{name: "a module that is not there", wanted: `"./missing"`},
		{name: "a name the app did not register", wanted: `"api"`},
		{name: "fs the app did not give", wanted: `"fs"`},
		{name: "no string", wanted: `1`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"other.js": command, "run.js": lines(
				`exports.definition = { short: "Run", long: "Run it." };`,
				`exports.command = () => {`,
				`  require(`+tc.wanted+`);`,
				`};`)}

			got := execute(t, scripts(files), cliche.Env{}, "run")

			failed := requireAs[*cliche.ScriptError](t, got.err)
			assert.Equal(t, "test:run.js", failed.File)
			assert.Equal(t, 3, failed.Line)
		})
	}
}

func TestScriptFaultHasItsPlace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		files  map[string]string
		file   string
		line   int
		column int
	}{
		{
			name: "a throw on the first line",
			files: map[string]string{"run.js": `exports.command = () => { throw new Error("no"); }; ` +
				`exports.definition = { short: "Run", long: "Run it." };`},
			file: "test:run.js", line: 1, column: 33,
		},
		{
			name: "an assignment to an undeclared name, refused by strict mode",
			files: map[string]string{"run.js": lines(`exports.definition = { short: "Run", long: "Run it." };`,
				`exports.command = () => {`, `  undeclared = 1;`, `};`)},
			file: "test:run.js", line: 3, column: 3,
		},
		{
			name: "a syntax error of a library module",
			files: map[string]string{"lib.js": "exports.x = ;\n", "run.js": lines(`const lib = require("./lib");`,
				`exports.definition = { short: "Run", long: "Run it." };`, `exports.command = () => ({});`)},
			file: "test:lib.js", line: 1, column: 13,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := execute(t, scripts(tc.files), cliche.Env{}, "run")

			failed := requireAs[*cliche.ScriptError](t, got.err)
			assert.Equal(t, [3]any{tc.file, tc.line, tc.column}, [3]any{failed.File, failed.Line, failed.Column})
		})
	}
}

func TestScriptThatIsNoCommandIsAScriptError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "no command", body: ``},
		{name: "a command that is no function", body: `exports.command = 1;`},
		{name: "a command that returns undefined", body: `exports.command = () => {};`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"run.js": lines(`exports.definition = { short: "Run", long: "Run it." };`, tc.body)}

			got := execute(t, scripts(files), cliche.Env{}, "run")

			failed := requireAs[*cliche.ScriptError](t, got.err)
			assert.Equal(t, "test:run.js", failed.File)
		})
	}
}

func TestCancelledContextInterruptsTheScript(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got := executeContext(t, ctx, scripts(map[string]string{"run.js": declared(`for (;;) {}`)}), cliche.Env{}, "run")

	interrupted := requireAs[*cliche.InterruptedError](t, got.err)
	assert.ErrorIs(t, interrupted, context.DeadlineExceeded)
}

func TestRunawayRecursionIsAScriptError(t *testing.T) {
	t.Parallel()
	files := map[string]string{"run.js": lines(`exports.definition = { short: "Run", long: "Run it." };`,
		`const deeper = (n) => deeper(n + 1) + 1;`, `exports.command = () => deeper(0);`)}

	got := execute(t, scripts(files), cliche.Env{}, "run")

	failed := requireAs[*cliche.ScriptError](t, got.err)
	assert.Equal(t, 2, failed.Line)
}
