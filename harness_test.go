package cliche_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

// outcome is what an app sees of one call: the value its run got, the error, and whether the script ran.
type outcome struct {
	value any
	err   error
	ran   bool
}

// app builds the cobra tree an app would: the merged catalog under a root of its own.
func app(config cliche.Config, env cliche.Env, got *outcome) *cobra.Command {
	root := &cobra.Command{Use: "app", SilenceErrors: true, SilenceUsage: true}
	root.CompletionOptions.DisableDefaultCmd = true
	cliche.Merge(config.Scan()).AddTo(root, func(cmd *cobra.Command, command *cliche.Command, in cliche.Input) error {
		got.ran = true
		value, err := cliche.Run(cmd.Context(), command, in, env)
		got.value = value
		return err
	})
	return root
}

func execute(t *testing.T, config cliche.Config, env cliche.Env, argv ...string) outcome {
	t.Helper()
	return executeContext(t, context.Background(), config, env, argv...)
}

func executeContext(t *testing.T, ctx context.Context, config cliche.Config, env cliche.Env,
	argv ...string,
) outcome {
	t.Helper()
	var got outcome
	root := app(config, env, &got)
	root.SetArgs(argv)
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	got.err = root.ExecuteContext(ctx)
	return got
}

// completion is what the cobra protocol of completion answers: the names offered and the directive.
type completion struct {
	names     []string
	directive cobra.ShellCompDirective
}

func complete(t *testing.T, config cliche.Config, argv ...string) completion {
	t.Helper()
	var got outcome
	root := app(config, cliche.Env{}, &got)
	out := new(bytes.Buffer)
	root.SetArgs(append([]string{cobra.ShellCompRequestCmd}, argv...))
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	require.NoError(t, root.Execute())
	require.False(t, got.ran)
	answer := completion{names: []string{}}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if directive, isDirective := strings.CutPrefix(line, ":"); isDirective {
			number, err := strconv.Atoi(directive)
			require.NoError(t, err)
			answer.directive = cobra.ShellCompDirective(number)
			continue
		}
		name, _, _ := strings.Cut(line, "\t")
		answer.names = append(answer.names, name)
	}
	return answer
}

func scripts(files map[string]string) cliche.Config {
	return cliche.Config{Sources: []cliche.Source{cliche.FS("test", mapFS(files))}}
}

func mapFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, text := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(text)}
	}
	return fsys
}

func lines(texts ...string) string {
	return strings.Join(texts, "\n") + "\n"
}

// declared is a command script with no arguments or flags and the given body of command.
func declared(body string) string {
	return lines(`exports.definition = { short: "Run", long: "Run it." };`,
		`exports.command = () => { `+body+` };`)
}

func object(pairs ...any) *cliche.Object {
	keys := make([]string, 0, len(pairs)/2)
	values := make([]any, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		keys = append(keys, pairs[i].(string))
		values = append(values, pairs[i+1])
	}
	return cliche.NewObject(keys, values)
}

func requireAs[T error](t *testing.T, err error) T {
	t.Helper()
	var target T
	require.ErrorAs(t, err, &target)
	return target
}

// record is a mapping of the app, as an SDK model would be: no type of cliche.
type record struct {
	keys   []string
	values map[string]any
}

func (r *record) Keys() []string {
	return r.keys
}

func (r *record) Get(key string) (any, bool) {
	value, found := r.values[key]
	return value, found
}

// items is a list of the app.
type items struct {
	values []any
}

func (l *items) Len() int {
	return len(l.values)
}

func (l *items) Index(i int) any {
	return l.values[i]
}

// appError is an error of the app with the fields a script reads.
type appError struct {
	code string
	err  error
}

func (e *appError) Error() string {
	return e.err.Error()
}

func (e *appError) Unwrap() error {
	return e.err
}

func (e *appError) ScriptFields() cliche.Map {
	return object("code", e.code, "message", e.err.Error())
}

// call is a call of a Func of the API module, as the app recorded it.
type call struct {
	name   string
	args   []any
	file   string
	line   int
	column int
}

type recorder struct {
	mu    sync.Mutex
	calls []call
}

func (r *recorder) Func(name string, answer func(args []any) (any, error)) cliche.Func {
	return func(ctx context.Context, args ...any) (any, error) {
		file, line, column := cliche.Place(ctx)
		r.mu.Lock()
		r.calls = append(r.calls, call{name: name, args: args, file: file, line: line, column: column})
		r.mu.Unlock()
		return answer(args)
	}
}

func (r *recorder) recorded() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func answering(value any) func([]any) (any, error) {
	return func([]any) (any, error) { return value, nil }
}

func failing(err error) func([]any) (any, error) {
	return func([]any) (any, error) { return nil, err }
}

func funcPointer(value any) uintptr {
	return reflect.ValueOf(value).Pointer()
}

var errSentinel = errors.New("the sentinel of the app")
