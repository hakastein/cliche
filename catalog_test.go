package cliche_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

const command = `exports.definition = { short: "Run", long: "Run it." };` + "\n" + `exports.command = () => ({});` + "\n"

// summary is what a root holds: the paths of its commands and of its broken paths, joined by spaces.
type summary struct {
	commands []string
	broken   []string
}

func summarize(roots []cliche.Root) []summary {
	summaries := []summary{}
	for _, root := range roots {
		held := summary{commands: []string{}, broken: []string{}}
		for _, word := range root.Words {
			for _, c := range word.Commands {
				held.commands = append(held.commands, strings.Join(c.Path, " "))
			}
			for _, b := range word.Broken {
				held.broken = append(held.broken, strings.Join(b.Path, " "))
			}
		}
		summaries = append(summaries, held)
	}
	return summaries
}

func TestScanReadsCommandPathsFromFilePaths(t *testing.T) {
	t.Parallel()
	deep := "a/b/c/d/e/f/g/h/run.js"
	tests := []struct {
		name  string
		files map[string]string
		want  summary
	}{
		{name: "a file and a directory of commands", files: map[string]string{"docs/map.js": command,
			"docs/list.js": command, "run.js": command},
			want: summary{commands: []string{"docs list", "docs map", "run"}, broken: []string{}}},
		{name: "a dot file and a dot directory", files: map[string]string{".hidden.js": command,
			".git/run.js": command, "run.js": command},
			want: summary{commands: []string{"run"}, broken: []string{}}},
		{name: "a library module and a file of another kind", files: map[string]string{"lib.js": "exports.x = 1;\n",
			"notes.txt": "text", "run.js": command},
			want: summary{commands: []string{"run"}, broken: []string{}}},
		{name: "a file and a directory of the same word", files: map[string]string{"docs.js": command,
			"docs/map.js": command},
			want: summary{commands: []string{}, broken: []string{"docs"}}},
		{name: "a library module beside a directory of the same word", files: map[string]string{
			"docs.js": "exports.x = 1;\n", "docs/map.js": command},
			want: summary{commands: []string{}, broken: []string{"docs"}}},
		{name: "a word out of the grammar", files: map[string]string{"Run.js": command, "_x/run.js": command},
			want: summary{commands: []string{}, broken: []string{"Run", "_x"}}},
		{name: "a path too deep", files: map[string]string{deep: command},
			want: summary{commands: []string{}, broken: []string{"a b c d e f g h"}}},
		{name: "an unreadable declaration", files: map[string]string{"run.js": "exports.definition = { short: 1 };\n"},
			want: summary{commands: []string{}, broken: []string{"run"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, []summary{tc.want}, summarize(scripts(tc.files).Scan()))
		})
	}
}

func TestBrokenPathIsAScriptErrorOfTheCall(t *testing.T) {
	t.Parallel()
	files := map[string]string{"docs.js": command, "docs/map.js": command}

	got := execute(t, scripts(files), cliche.Env{}, "docs")

	failed := requireAs[*cliche.ScriptError](t, got.err)
	assert.Equal(t, "test:docs.js", failed.File)
}

func TestMergeKeepsThePathOfTheEarlierRoot(t *testing.T) {
	t.Parallel()
	first := cliche.FS("first", mapFS(map[string]string{"docs/map.js": command, "run.js": command}))
	tests := []struct {
		name     string
		second   map[string]string
		commands []string
		warnings []cliche.Warning
	}{
		{
			name:     "the same path",
			second:   map[string]string{"docs/map.js": command, "docs/list.js": command},
			commands: []string{"first:docs/map.js", "first:run.js", "second:docs/list.js"},
			warnings: []cliche.Warning{{Path: []string{"docs", "map"}, File: "second:docs/map.js",
				Kept: "first:docs/map.js"}},
		},
		{
			name:     "a path that starts another is no conflict",
			second:   map[string]string{"docs.js": command, "run/fast.js": command},
			commands: []string{"first:docs/map.js", "first:run.js", "second:docs.js", "second:run/fast.js"},
			warnings: nil,
		},
		{
			name:     "no conflict",
			second:   map[string]string{"other.js": command},
			commands: []string{"first:docs/map.js", "first:run.js", "second:other.js"},
			warnings: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := cliche.Config{Sources: []cliche.Source{first, cliche.FS("second", mapFS(tc.second))}}

			catalog := cliche.Merge(config.Scan())

			files := []string{}
			for _, c := range catalog.Commands {
				files = append(files, c.File())
			}
			assert.Equal(t, tc.commands, files)
			for i := range catalog.Warnings {
				catalog.Warnings[i].Message = ""
			}
			assert.Equal(t, tc.warnings, catalog.Warnings)
		})
	}
}

func TestEarlierRootRunsItsCommandOnAConflict(t *testing.T) {
	t.Parallel()
	config := cliche.Config{Sources: []cliche.Source{
		cliche.FS("first", mapFS(map[string]string{"run.js": declared(`return "first";`)})),
		cliche.FS("second", mapFS(map[string]string{"run.js": declared(`return "second";`)})),
	}}

	got := execute(t, config, cliche.Env{}, "run")

	require.NoError(t, got.err)
	assert.Equal(t, "first", got.value)
}

func TestBuiltinRootComesFirstAndIsBuiltin(t *testing.T) {
	t.Parallel()
	config := cliche.Config{
		Builtin: mapFS(map[string]string{"run.js": command}),
		Sources: []cliche.Source{cliche.FS("test", mapFS(map[string]string{"run.js": command, "own.js": command}))},
	}

	catalog := cliche.Merge(config.Scan())

	builtin := map[string]bool{}
	for _, c := range catalog.Commands {
		builtin[c.File()] = c.Builtin()
	}
	assert.Equal(t, map[string]bool{"builtin:run.js": true, "test:own.js": false}, builtin)
}

// firstWordOwned is the rule of ytrack, written by an app over Scan: a first word belongs to the first root that has
// it, and no later root adds to it.
func firstWordOwned(roots []cliche.Root) []string {
	owner := map[string]string{}
	var commands []string
	for _, root := range roots {
		for _, word := range root.Words {
			if held, owned := owner[word.Word]; owned && held != root.Name {
				continue
			}
			owner[word.Word] = root.Name
			for _, c := range word.Commands {
				commands = append(commands, c.File())
			}
		}
	}
	return commands
}

func TestAppRuleOfRootsIsWrittenOverScan(t *testing.T) {
	t.Parallel()
	config := cliche.Config{
		Builtin: mapFS(map[string]string{"docs/map.js": command}),
		Sources: []cliche.Source{cliche.FS("test", mapFS(map[string]string{"docs/list.js": command,
			"run.js": command}))},
	}

	assert.Equal(t, []string{"builtin:docs/map.js", "test:run.js"}, firstWordOwned(config.Scan()))
}

func scriptsAt(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		at := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(at), 0o755))
		require.NoError(t, os.WriteFile(at, []byte(text), 0o644))
	}
}

func rootNames(roots []cliche.Root) []string {
	names := []string{}
	for _, root := range roots {
		names = append(names, root.Name)
	}
	return names
}

func TestDefaultSourcesAreTheNearestProjectAndTheUser(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	project := filepath.Join(base, "work", "project")
	nested := filepath.Join(project, "src", "deep")
	inHome := filepath.Join(home, "code")
	scriptsAt(t, filepath.Join(home, ".app", "scripts"), map[string]string{"user.js": command})
	scriptsAt(t, filepath.Join(project, ".app", "scripts"), map[string]string{"project.js": command})
	scriptsAt(t, filepath.Join(base, "work", ".app", "scripts"), map[string]string{"far.js": command})
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.MkdirAll(inHome, 0o755))
	user := filepath.Join(home, ".app", "scripts")
	tests := []struct {
		name       string
		workingDir string
		home       string
		want       []string
	}{
		{name: "the nearest project above the working directory", workingDir: nested, home: home,
			want: []string{filepath.Join(project, ".app", "scripts"), user}},
		{name: "the user's own as the nearest", workingDir: inHome, home: home, want: []string{user}},
		{name: "no working directory", workingDir: "", home: home, want: []string{user}},
		{name: "no home", workingDir: nested, home: "", want: []string{filepath.Join(project, ".app", "scripts")}},
		{name: "neither", workingDir: "", home: "", want: []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := cliche.Config{Sources: cliche.DefaultSources("app", tc.workingDir, tc.home)}

			assert.Equal(t, tc.want, rootNames(config.Scan()))
		})
	}
}

func TestSourcesNilAreTheDefaultsAndEmptyAreNone(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	project := filepath.Join(base, "project")
	scriptsAt(t, filepath.Join(home, ".app", "scripts"), map[string]string{"user.js": command})
	scriptsAt(t, filepath.Join(project, ".app", "scripts"), map[string]string{"project.js": command})
	t.Setenv("HOME", home)
	t.Chdir(project)
	builtin := fstest.MapFS{"run.js": &fstest.MapFile{Data: []byte(command)}}
	tests := []struct {
		name    string
		sources []cliche.Source
		want    []string
	}{
		{name: "nil", sources: nil, want: []string{"builtin", filepath.Join(project, ".app", "scripts"),
			filepath.Join(home, ".app", "scripts")}},
		{name: "empty", sources: []cliche.Source{}, want: []string{"builtin"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := cliche.Config{Name: "app", Builtin: builtin, Sources: tc.sources}

			assert.Equal(t, tc.want, rootNames(config.Scan()))
		})
	}
}

func TestDirNamesItsFilesByAbsolutePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	scriptsAt(t, dir, map[string]string{"docs/map.js": command})

	catalog := cliche.Merge(cliche.Config{Sources: []cliche.Source{cliche.Dir(dir)}}.Scan())

	require.Len(t, catalog.Commands, 1)
	assert.Equal(t, filepath.Join(dir, "docs", "map.js"), catalog.Commands[0].File())
}
