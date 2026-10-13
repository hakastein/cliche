package cliche_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

// bridged runs body as the command of a script with the module api.
func bridged(t *testing.T, api any, body string) outcome {
	t.Helper()
	files := map[string]string{"run.js": lines(`const api = require("api");`,
		`exports.definition = { short: "Run", long: "Run it." };`, `exports.command = () => { `+body+` };`)}
	return execute(t, scripts(files), cliche.Env{Modules: map[string]any{"api": api}, Node: cliche.NodeBuffer}, "run")
}

func document() *record {
	return &record{keys: []string{"id", "tags", "empty", "inner"}, values: map[string]any{
		"id": "DEV-1", "tags": &items{values: []any{"a", "b"}}, "empty": nil,
		"inner": &record{keys: []string{"x"}, values: map[string]any{"x": int64(1)}},
	}}
}

func TestMapAndListOfTheAppComeBackAsTheSameValues(t *testing.T) {
	t.Parallel()
	doc := document()
	tests := []struct {
		name string
		body string
		want func(t *testing.T, value any, calls []call)
	}{
		{
			name: "returned",
			body: `return api.get();`,
			want: func(t *testing.T, value any, _ []call) { assert.Same(t, doc, value) },
		},
		{
			name: "nested in a new object",
			body: `const d = api.get(); return { doc: d, tags: d.tags, own: 1 };`,
			want: func(t *testing.T, value any, _ []call) {
				assert.Equal(t, []string{"doc", "tags", "own"}, value.(*cliche.Object).Keys())
				held, _ := value.(*cliche.Object).Get("doc")
				assert.Same(t, doc, held)
				tags, _ := value.(*cliche.Object).Get("tags")
				assert.Same(t, doc.values["tags"], tags)
			},
		},
		{
			name: "passed back as an argument, nested",
			body: `const d = api.get(); api.take(d.inner, [d.tags]); return 1;`,
			want: func(t *testing.T, _ any, calls []call) {
				require.Len(t, calls, 2)
				assert.Same(t, doc.values["inner"], calls[1].args[0])
				assert.Same(t, doc.values["tags"], calls[1].args[1].([]any)[0])
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := &recorder{}
			api := object("get", calls.Func("get", answering(doc)), "take", calls.Func("take", answering(nil)))

			got := bridged(t, api, tc.body)

			require.NoError(t, got.err)
			tc.want(t, got.value, calls.recorded())
		})
	}
}

func TestFuncComesBackAsTheSameFunc(t *testing.T) {
	t.Parallel()
	calls := &recorder{}
	get := calls.Func("get", answering(nil))
	api := object("get", get, "take", calls.Func("take", answering(nil)))

	got := bridged(t, api, `api.take(api.get); return [api.get];`)

	require.NoError(t, got.err)
	assert.Equal(t, funcPointer(get), funcPointer(calls.recorded()[0].args[0]))
	assert.Equal(t, funcPointer(get), funcPointer(got.value.([]any)[0]))
}

func TestScriptReadsAMapOfTheAppAsAReadOnlyObject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want any
	}{
		{name: "keys in the order of the app", body: `return Object.keys(api.get());`,
			want: []any{"id", "tags", "empty", "inner"}},
		{name: "null and a missing key apart", body: `const d = api.get(); ` +
			`return [d.empty === null, d.missing === undefined, "empty" in d, "missing" in d];`,
			want: []any{true, true, true, false}},
		{name: "the same object on a second read", body: `const d = api.get(); return d.inner === d.inner && d.tags === d.tags;`,
			want: true},
		{name: "a list as an array", body: `const t = api.get().tags; return [Array.isArray(t), t.length, t.map((x) => x + "!")];`,
			want: []any{true, int64(2), []any{"a!", "b!"}}},
		{name: "a copy with spread", body: `return { ...api.get().inner, y: 2 };`,
			want: object("x", int64(1), "y", int64(2))},
		{name: "JSON of a map", body: `return JSON.stringify(api.get());`,
			want: `{"id":"DEV-1","tags":["a","b"],"empty":null,"inner":{"x":1}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bridged(t, object("get", (&recorder{}).Func("get", answering(document()))), tc.body)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestWriteToAValueOfTheAppIsATypeError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		write string
	}{
		{name: "a set key", write: `d.id = "x";`},
		{name: "an added key", write: `d.added = 1;`},
		{name: "a deleted key", write: `delete d.id;`},
		{name: "a push", write: `d.tags.push("c");`},
		{name: "a set index", write: `d.tags[0] = "c";`},
		{name: "a set length", write: `d.tags.length = 0;`},
		{name: "a key of an Object of the app", write: `api.name = "x";`},
		{name: "a field of a thrown error", write: `try { api.fail(); } catch (e) { e.code = "x"; }`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := &recorder{}
			api := object("name", "api", "get", calls.Func("get", answering(document())),
				"fail", calls.Func("fail", failing(&appError{code: "denied", err: errSentinel})))

			got := bridged(t, api, `const d = api.get(); try { `+tc.write+` return "written"; } `+
				`catch (e) { return e instanceof TypeError; }`)

			require.NoError(t, got.err)
			assert.Equal(t, true, got.value)
		})
	}
}

func TestNumbersCrossTheBridgeExactly(t *testing.T) {
	t.Parallel()
	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	tests := []struct {
		name   string
		answer any
		body   string
		want   any
	}{
		{name: "an int64 beyond 2^53 becomes a BigInt", answer: int64(1<<53 + 1),
			body: `const n = api.get(); return [typeof n, String(n)];`, want: []any{"bigint", "9007199254740993"}},
		{name: "an int64 within 2^53 stays a number", answer: int64(1 << 53),
			body: `return typeof api.get();`, want: "number"},
		{name: "a big.Int becomes a BigInt", answer: huge, body: `return api.get() + 1n;`,
			want: new(big.Int).Add(huge, big.NewInt(1))},
		{name: "a whole number of the script is an int64", answer: nil, body: `return 3;`, want: int64(3)},
		{name: "a fraction is a float64", answer: nil, body: `return 0.5;`, want: 0.5},
		{name: "a BigInt of the script is a big.Int", answer: nil, body: `return 5n;`, want: big.NewInt(5)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bridged(t, object("get", (&recorder{}).Func("get", answering(tc.answer))), tc.body)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestBytesCrossTheBridgeAsCopies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want any
	}{
		{name: "bytes of the app are a Buffer", body: `const b = api.get(); return [Buffer.isBuffer(b), b.toString("utf8")];`,
			want: []any{true, "hi"}},
		{name: "a Buffer of the script", body: `return Buffer.from("hey");`, want: []byte("hey")},
		{name: "a Uint8Array of the script", body: `return new Uint8Array([1, 2]);`, want: []byte{1, 2}},
		{name: "an ArrayBuffer of the script", body: `return new Uint8Array([3]).buffer;`, want: []byte{3}},
		{name: "a write to the Buffer leaves the bytes of the app", body: `const b = api.get(); b[0] = 0; return api.get();`,
			want: []byte("hi")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bridged(t, object("get", (&recorder{}).Func("get", answering([]byte("hi")))), tc.body)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestValueTheBridgeDoesNotCarryIsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "a Date", body: `return { at: new Date(0) };`},
		{name: "a function", body: `return [() => 1];`},
		{name: "a symbol", body: `return { s: Symbol("s") };`},
		{name: "undefined", body: `return undefined;`},
		{name: "undefined in an array", body: `return [undefined];`},
		{name: "an object of a class", body: `return new (class A {})();`},
		{name: "a Map of JS", body: `return new Map();`},
		{name: "a value that holds itself", body: `const a = []; a.push(a); return a;`},
		{name: "a length past its items", body: `const a = []; a.length = 4294967295; return a;`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bridged(t, object(), tc.body)

			failed := requireAs[*cliche.ScriptError](t, got.err)
			assert.Equal(t, "test:run.js", failed.File)
		})
	}
}

func TestArgumentTheBridgeDoesNotCarryIsATypeErrorBeforeTheCall(t *testing.T) {
	t.Parallel()
	calls := &recorder{}
	api := object("take", calls.Func("take", answering(nil)))

	got := bridged(t, api, `try { api.take({ at: new Date(0) }); } catch (e) { return e instanceof TypeError; }`)

	require.NoError(t, got.err)
	assert.Equal(t, true, got.value)
	assert.Empty(t, calls.recorded())
}

func TestScriptBuiltValuesReachTheAppInOrder(t *testing.T) {
	t.Parallel()
	calls := &recorder{}
	api := object("take", calls.Func("take", answering(nil)))

	got := bridged(t, api, `api.take({ b: 1, a: null, gone: undefined, list: ["x", true] }); return 1;`)

	require.NoError(t, got.err)
	assert.Equal(t, []any{object("b", int64(1), "a", nil, "list", []any{"x", true})}, calls.recorded()[0].args)
}

func TestGetterIsComputedOnEveryRead(t *testing.T) {
	t.Parallel()
	count := int64(0)
	api := object("count", cliche.Getter(func(context.Context) (any, error) { count++; return count, nil }),
		"broken", cliche.Getter(func(context.Context) (any, error) { return nil, errSentinel }))

	got := bridged(t, api, `const first = api.count; const second = api.count; `+
		`let caught; try { api.broken; } catch (e) { caught = e instanceof Error; } return [first, second, caught];`)

	require.NoError(t, got.err)
	assert.Equal(t, []any{int64(1), int64(2), true}, got.value)
}

func TestScriptGetterReadByTheBridgeThrowsIntoTheScript(t *testing.T) {
	t.Parallel()
	calls := &recorder{}
	api := object("take", calls.Func("take", answering(nil)))

	got := bridged(t, api, `const bad = { get x() { throw new Error("getter"); } }; `+
		`try { api.take(bad); } catch (e) { return e.message; }`)

	require.NoError(t, got.err)
	assert.Equal(t, "getter", got.value)
	assert.Empty(t, calls.recorded())
}

func TestScriptGetterOfTheReturnedValueIsAScriptError(t *testing.T) {
	t.Parallel()
	got := bridged(t, object(), `return { get x() { throw new Error("getter"); } };`)

	requireAs[*cliche.ScriptError](t, got.err)
}

func TestFuncKnowsThePlaceThatCalledIt(t *testing.T) {
	t.Parallel()
	calls := &recorder{}
	files := map[string]string{"run.js": lines(`const api = require("api");`,
		`exports.definition = { short: "Run", long: "Run it." };`,
		`exports.command = () => {`,
		`  return api.where();`,
		`};`)}

	got := execute(t, scripts(files), cliche.Env{Modules: map[string]any{
		"api": object("where", calls.Func("where", answering(nil)))}}, "run")

	require.NoError(t, got.err)
	held := calls.recorded()[0]
	assert.Equal(t, [3]any{"test:run.js", 4, 19}, [3]any{held.file, held.line, held.column})
}

func TestValueOfTheAppOutsideTheBridgeIsATypeError(t *testing.T) {
	t.Parallel()
	api := object("get", (&recorder{}).Func("get", answering(3)))

	got := bridged(t, api, `try { api.get(); } catch (e) { return e instanceof TypeError; }`)

	require.NoError(t, got.err)
	assert.Equal(t, true, got.value)
}

func TestSliceOfTheAppIsAnArrayOfTheScript(t *testing.T) {
	t.Parallel()
	api := object("get", (&recorder{}).Func("get", answering([]any{"a", int64(1)})))

	got := bridged(t, api, `const a = api.get(); a.push("b"); return a;`)

	require.NoError(t, got.err)
	assert.Equal(t, []any{"a", int64(1), "b"}, got.value)
}

func TestDescriptorOfAGetterOfTheAppComputesNothing(t *testing.T) {
	t.Parallel()
	count := 0
	api := object("count", cliche.Getter(func(context.Context) (any, error) { count++; return int64(count), nil }))

	got := bridged(t, api, `const d = Object.getOwnPropertyDescriptor(api, "count"); `+
		`return [typeof d.get, Object.keys(api), Object.isFrozen(api)];`)

	require.NoError(t, got.err)
	assert.Equal(t, []any{"function", []any{"count"}, true}, got.value)
	assert.Equal(t, 0, count)
}
