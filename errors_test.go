package cliche_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

func failingAPI(err error) (*recorder, *cliche.Object) {
	calls := &recorder{}
	return calls, object("fail", calls.Func("fail", failing(err)))
}

func TestScriptReadsTheFieldsOfAnErrorOfTheApp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		body string
		want any
	}{
		{name: "the fields", err: &appError{code: "denied", err: errSentinel},
			body: `try { api.fail(); } catch (e) { return [e.code, e.message, e instanceof Error, Object.keys(e)]; }`,
			want: []any{"denied", errSentinel.Error(), true, []any{"code", "message"}}},
		{name: "message of an error with no fields", err: errSentinel,
			body: `try { api.fail(); } catch (e) { return [e.message, e.code === undefined, e instanceof Error]; }`,
			want: []any{errSentinel.Error(), true, true}},
		{name: "the string form of Error", err: errSentinel,
			body: `try { api.fail(); } catch (e) { return String(e); }`, want: "Error: " + errSentinel.Error()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, api := failingAPI(tc.err)

			got := bridged(t, api, tc.body)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestUncaughtErrorOfTheAppIsTheSameError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "uncaught", body: `api.fail();`},
		{name: "rethrown", body: `try { api.fail(); } catch (e) { throw e; }`},
		{name: "rethrown from a nested catch", body: `try { try { api.fail(); } finally { } } catch (e) { const kept = e; throw kept; }`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure := &appError{code: "denied", err: errSentinel}
			_, api := failingAPI(failure)

			got := bridged(t, api, tc.body)

			assert.Same(t, failure, got.err)
			assert.ErrorIs(t, got.err, errSentinel)
		})
	}
}

func TestErrorBuiltByTheScriptIsAScriptError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "a new Error", body: `throw new Error("own");`},
		{name: "the fields copied onto a new Error",
			body: `try { api.fail(); } catch (e) { throw Object.assign(new Error(e.message), { code: e.code }); }`},
		{name: "a wrapper with a cause", body: `try { api.fail(); } catch (e) { throw { cause: e }; }`},
		{name: "a spread copy", body: `try { api.fail(); } catch (e) { throw { ...e }; }`},
		{name: "a string", body: `throw "text";`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, api := failingAPI(&appError{code: "denied", err: errSentinel})

			got := bridged(t, api, tc.body)

			failed := requireAs[*cliche.ScriptError](t, got.err)
			assert.Equal(t, [2]any{"test:run.js", 3}, [2]any{failed.File, failed.Line})
			assert.NotErrorIs(t, got.err, errSentinel)
		})
	}
}

func TestEnvErrorTranslatesAFetchErrorBeforeItIsThrown(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a body longer than allowed"))
	}))
	t.Cleanup(server.Close)
	translate := func(err error) error { return &appError{code: "upstream", err: err} }
	tests := []struct {
		name  string
		catch bool
	}{
		{name: "caught", catch: true},
		{name: "uncaught", catch: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := `fetch(url, { timeout: 5000, maxBytes: 4, redirect: "follow" })`
			body := call + `;`
			if tc.catch {
				body = `try { ` + call + `; } catch (e) { return e.code; }`
			}
			files := fetchScript(body)
			env := cliche.Env{Node: cliche.NodeFetch, Error: translate}

			got := execute(t, scripts(files), env, "run", server.URL)

			if tc.catch {
				require.NoError(t, got.err)
				assert.Equal(t, "upstream", got.value)
				return
			}
			translated := requireAs[*appError](t, got.err)
			assert.Equal(t, "upstream", translated.code)
			fetchFailed := requireAs[*cliche.FetchError](t, got.err)
			assert.Equal(t, server.URL, fetchFailed.URL)
		})
	}
}

func TestErrorOfTheAppIsNotTranslated(t *testing.T) {
	t.Parallel()
	_, api := failingAPI(errSentinel)
	files := map[string]string{"run.js": lines(`const api = require("api");`,
		`exports.definition = { short: "Run", long: "Run it." };`, `exports.command = () => api.fail();`)}
	env := cliche.Env{Modules: map[string]any{"api": api},
		Error: func(err error) error { return errors.New("translated") }}

	got := execute(t, scripts(files), env, "run")

	assert.Same(t, errSentinel, got.err)
}
