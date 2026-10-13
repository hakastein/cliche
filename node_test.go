package cliche_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

// fetchScript is a command that takes a url and runs body with it.
func fetchScript(body string) map[string]string {
	return map[string]string{"run.js": lines(
		`exports.definition = { short: "Run", long: "Run it.", args: [{ name: "url", type: "string", usage: "a url" }] };`,
		`exports.command = (url) => {`,
		`  `+body,
		`};`)}
}

// fsScript is a command that takes a directory and runs body with fs.
func fsScript(body string) map[string]string {
	return map[string]string{"run.js": lines(
		`const fs = require("fs");`,
		`exports.definition = { short: "Run", long: "Run it.", args: [{ name: "dir", type: "path", usage: "a dir" }] };`,
		`exports.command = (dir) => {`,
		`  `+body,
		`};`)}
}

func TestNodeSubsetIsOnlyWhatTheAppGives(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		node cliche.Node
		want any
	}{
		{name: "nothing by default", node: 0, want: []any{"undefined", "undefined"}},
		{name: "Buffer", node: cliche.NodeBuffer, want: []any{"undefined", "function"}},
		{name: "fetch", node: cliche.NodeFetch, want: []any{"function", "undefined"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"run.js": declared(`return [typeof fetch, typeof Buffer];`)}

			got := execute(t, scripts(files), cliche.Env{Node: tc.node}, "run")

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestScriptReadsAndWritesFilesWithFs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want any
	}{
		{name: "a file written and read",
			body: `fs.writeFileSync(dir + "/a.txt", "hi"); return [fs.readFileSync(dir + "/a.txt", "utf8"), ` +
				`Buffer.isBuffer(fs.readFileSync(dir + "/a.txt")), fs.existsSync(dir + "/a.txt"), fs.existsSync(dir + "/b")];`,
			want: []any{"hi", true, true, false}},
		{name: "bytes written",
			body: `fs.writeFileSync(dir + "/a.bin", Buffer.from([1, 2])); return fs.readFileSync(dir + "/a.bin");`,
			want: []byte{1, 2}},
		{name: "a directory made and listed",
			body: `const first = fs.mkdirSync(dir + "/x/y", { recursive: true }); fs.writeFileSync(dir + "/x/f", ""); ` +
				`return [first === dir + "/x", fs.readdirSync(dir + "/x").sort(), fs.statSync(dir + "/x").isDirectory(), ` +
				`fs.statSync(dir + "/x/f").size, fs.statSync(dir + "/none", { throwIfNoEntry: false }) === undefined];`,
			want: []any{true, []any{"f", "y"}, true, int64(0), true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := execute(t, scripts(fsScript(tc.body)), cliche.Env{Node: cliche.NodeFS | cliche.NodeBuffer}, "run", t.TempDir())

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestErrorOfFsHasTheShapeOfNode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tests := []struct {
		name string
		body string
		want any
	}{
		{name: "a missing file", body: `try { fs.readFileSync(dir + "/none"); } catch (e) { ` +
			`return [e.code, e.syscall, e.path === dir + "/none", e instanceof Error]; }`,
			want: []any{"ENOENT", "open", true, true}},
		{name: "a directory read", body: `try { fs.readFileSync(dir); } catch (e) { return [e.code, e.syscall]; }`,
			want: []any{"EISDIR", "read"}},
		{name: "a directory made twice", body: `try { fs.mkdirSync(dir); } catch (e) { return [e.code, e.syscall]; }`,
			want: []any{"EEXIST", "mkdir"}},
		{name: "an option fs does not take", body: `try { fs.readFileSync(dir, { flag: "r" }); } ` +
			`catch (e) { return e instanceof TypeError; }`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := execute(t, scripts(fsScript(tc.body)), cliche.Env{Node: cliche.NodeFS}, "run", dir)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestUncaughtErrorOfFsIsAFileError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tests := []struct {
		name string
		body string
	}{
		{name: "uncaught", body: `fs.readFileSync(dir + "/none");`},
		{name: "rethrown", body: `try { fs.readFileSync(dir + "/none"); } catch (e) { throw e; }`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := execute(t, scripts(fsScript(tc.body)), cliche.Env{Node: cliche.NodeFS}, "run", dir)

			failed := requireAs[*cliche.FileError](t, got.err)
			assert.Equal(t, [3]string{"ENOENT", "open", filepath.Join(dir, "none")},
				[3]string{failed.Code, failed.Syscall, failed.Path})
		})
	}
}

func TestFetchGivesAResponseOfAnyStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		want   any
	}{
		{name: "a success", status: http.StatusOK, want: []any{int64(200), true, "OK", "said"}},
		{name: "a missing page", status: http.StatusNotFound, want: []any{int64(404), false, "Not Found", "said"}},
		{name: "a failure of the server", status: http.StatusBadGateway,
			want: []any{int64(502), false, "Bad Gateway", "said"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("said"))
			}))
			t.Cleanup(server.Close)
			files := fetchScript(`const r = fetch(url, { timeout: 5000, maxBytes: 64, redirect: "follow" }); ` +
				`return [r.status, r.ok, r.statusText, r.text()];`)

			got := execute(t, scripts(files), cliche.Env{Node: cliche.NodeFetch}, "run", server.URL)

			require.NoError(t, got.err)
			assert.Equal(t, tc.want, got.value)
		})
	}
}

func TestFetchSendsNoCredentials(t *testing.T) {
	t.Parallel()
	seen := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
	}))
	t.Cleanup(server.Close)
	files := fetchScript(`return fetch(url, { timeout: 5000, maxBytes: 64, redirect: "follow" }).status;`)

	got := execute(t, scripts(files), cliche.Env{Node: cliche.NodeFetch}, "run", server.URL)

	require.NoError(t, got.err)
	header := <-seen
	assert.Equal(t, [2]string{}, [2]string{header.Get("Authorization"), header.Get("Cookie")})
}

func TestFetchThatGetsNoWholeResponseIsAFetchError(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	long := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("longer than four bytes"))
	}))
	t.Cleanup(long.Close)
	tests := []struct {
		name string
		url  string
	}{
		{name: "a timeout", url: slow.URL},
		{name: "a body longer than maxBytes", url: long.URL},
		{name: "no server", url: "http://127.0.0.1:1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := fetchScript(`return fetch(url, { timeout: 100, maxBytes: 4, redirect: "follow" });`)

			got := execute(t, scripts(files), cliche.Env{Node: cliche.NodeFetch}, "run", tc.url)

			failed := requireAs[*cliche.FetchError](t, got.err)
			assert.Equal(t, tc.url, failed.URL)
		})
	}
}

func TestFetchRefusesAnOptionItDoesNotTake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		options string
	}{
		{name: "a method other than GET", options: `{ timeout: 1, maxBytes: 1, redirect: "follow", method: "POST" }`},
		{name: "headers", options: `{ timeout: 1, maxBytes: 1, redirect: "follow", headers: {} }`},
		{name: "no timeout", options: `{ maxBytes: 1, redirect: "follow" }`},
		{name: "no redirect", options: `{ timeout: 1, maxBytes: 1 }`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := fetchScript(`try { fetch(url, ` + tc.options + `); } catch (e) { return e instanceof TypeError; }`)

			got := execute(t, scripts(files), cliche.Env{Node: cliche.NodeFetch}, "run", "http://127.0.0.1:1")

			require.NoError(t, got.err)
			assert.Equal(t, true, got.value)
		})
	}
}

func TestFsWritesWhereTheScriptSays(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	got := execute(t, scripts(fsScript(`fs.writeFileSync(dir + "/out.txt", "x"); return 1;`)),
		cliche.Env{Node: cliche.NodeFS}, "run", dir)

	require.NoError(t, got.err)
	written, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(written))
}
