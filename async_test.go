package cliche_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

// asyncAPI is a module of async functions of the app; start runs work in a goroutine of the app, which settles started
// at once and result after ack.
func asyncAPI() *cliche.Object {
	acked := make(chan struct{})
	return object(
		"twice", cliche.Async(func(_ context.Context, args ...any) (any, error) { return args[0].(int64) * 2, nil }),
		"fail", cliche.Async(func(context.Context, ...any) (any, error) {
			return nil, &appError{code: "E_APP", err: errSentinel}
		}),
		"odd", cliche.Async(func(context.Context, ...any) (any, error) { return struct{}{}, nil }),
		"settled", cliche.Func(func(context.Context, ...any) (any, error) {
			settled := new(cliche.Promise)
			settled.Settle("first", nil)
			settled.Settle("second", nil)
			return settled, nil
		}),
		"start", cliche.Func(func(ctx context.Context, _ ...any) (any, error) {
			started, result := new(cliche.Promise), new(cliche.Promise)
			go func() {
				started.Settle("started", nil)
				select {
				case <-acked:
					result.Settle("done", nil)
				case <-ctx.Done():
					result.Settle(nil, ctx.Err())
				}
			}()
			return object("started", started, "result", result), nil
		}),
		"ack", cliche.Func(func(context.Context, ...any) (any, error) { close(acked); return cliche.Undefined{}, nil }),
	)
}

func TestAsyncCommandAnswersWhatItsPromiseFulfils(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want any
	}{
		{name: "an async command", body: `return 1;`, want: int64(1)},
		{name: "an async function of the app", body: `return (await api.twice(2)) + 1;`, want: int64(5)},
		{name: "an error of an async function, caught", body: `try { await api.fail(); } catch (e) { return e.code; }`,
			want: "E_APP"},
		{name: "a value of an async function the bridge does not carry, caught",
			body: `try { await api.odd(); } catch (e) { return e instanceof TypeError; }`, want: true},
		{name: "a promise of the app settled twice", body: `return await api.settled();`, want: "first"},
		{name: "promises of the app a goroutine of the app settles",
			body: `const run = api.start(); const started = await run.started; api.ack(); ` +
				`return [started, await run.result];`,
			want: []any{"started", "done"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"run.js": lines(`const api = require("api");`,
				`exports.definition = { short: "Run", long: "Run it." };`,
				`exports.command = async () => { `+tc.body+` };`)}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			got := executeContext(t, ctx, scripts(files), cliche.Env{Modules: map[string]any{"api": asyncAPI()}}, "run")

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestUnsettledOrRejectedCommandPromiseIsAScriptError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		line int
	}{
		{name: "a throw after an await", body: `await api.twice(1);` + "\n" + `  throw new Error("no");`, line: 5},
		{name: "a promise of the script nothing settles after a settled promise of the app",
			body: `await api.settled(); await new Promise(() => {});`},
		{name: "a promise of the script nothing settles", body: `await new Promise(() => {});`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"run.js": lines(`const api = require("api");`,
				`exports.definition = { short: "Run", long: "Run it." };`,
				`exports.command = async () => {`, `  `+tc.body, `};`)}

			got := execute(t, scripts(files), cliche.Env{Modules: map[string]any{"api": asyncAPI()}}, "run")

			failed := requireAs[*cliche.ScriptError](t, got.err)
			assert.Equal(t, [2]any{"test:run.js", tc.line}, [2]any{failed.File, failed.Line})
		})
	}
}

func TestUncaughtErrorOfAsyncFunctionIsThatError(t *testing.T) {
	t.Parallel()
	files := map[string]string{"run.js": lines(`const api = require("api");`,
		`exports.definition = { short: "Run", long: "Run it." };`, `exports.command = async () => api.fail();`)}

	got := execute(t, scripts(files), cliche.Env{Modules: map[string]any{"api": asyncAPI()}}, "run")

	assert.ErrorIs(t, got.err, errSentinel)
}

func TestAsyncFunctionsRunInParallel(t *testing.T) {
	t.Parallel()
	var arrived sync.WaitGroup
	arrived.Add(2)
	together := make(chan struct{})
	go func() { arrived.Wait(); close(together) }()
	// meet answers only once both calls run at once: one after the other, the first waits until the deadline.
	meet := cliche.Async(func(ctx context.Context, args ...any) (any, error) {
		arrived.Done()
		select {
		case <-together:
			return args[0], nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	files := map[string]string{"run.js": lines(`const api = require("api");`,
		`exports.definition = { short: "Run", long: "Run it." };`,
		`exports.command = async () => await Promise.all([api.meet("a"), api.meet("b")]);`)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := executeContext(t, ctx, scripts(files), cliche.Env{Modules: map[string]any{"api": object("meet", meet)}}, "run")

	require.NoError(t, got.err)
	assert.Equal(t, []any{"a", "b"}, got.value)
}

func TestRunReturnsAfterTheWorkOfAsyncFunctionsEnds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command string
		want    any
		wantErr error
	}{
		{name: "the context cancelled while the command waits",
			command: `async () => { api.hold(); api.cancel(); await api.signal(); }`, wantErr: context.Canceled},
		{name: "work the command did not wait for", command: `() => { api.hold(); return 1; }`, want: int64(1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			api := object(
				"hold", cliche.Async(func(ctx context.Context, _ ...any) (any, error) {
					<-ctx.Done()
					// The pause gives a Run that does not wait for its work the time to return first.
					time.Sleep(20 * time.Millisecond)
					close(finished)
					return nil, nil
				}),
				"signal", cliche.Func(func(context.Context, ...any) (any, error) { return new(cliche.Promise), nil }),
				"cancel", cliche.Func(func(context.Context, ...any) (any, error) { cancel(); return cliche.Undefined{}, nil }),
			)
			files := map[string]string{"run.js": lines(`const api = require("api");`,
				`exports.definition = { short: "Run", long: "Run it." };`, `exports.command = `+tc.command+`;`)}

			got := executeContext(t, ctx, scripts(files), cliche.Env{Modules: map[string]any{"api": api}}, "run")

			assert.ErrorIs(t, got.err, tc.wantErr)
			assert.Equal(t, tc.want, got.value)
			assert.True(t, isClosed(finished))
		})
	}
}

func isClosed(done chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}
