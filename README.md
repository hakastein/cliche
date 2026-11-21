# cliche

[![Go Reference](https://pkg.go.dev/badge/github.com/hakastein/cliche.svg)](https://pkg.go.dev/github.com/hakastein/cliche)
[![CI](https://github.com/hakastein/cliche/actions/workflows/ci.yml/badge.svg)](https://github.com/hakastein/cliche/actions/workflows/ci.yml)

cliche is a Go framework that turns JavaScript command scripts into commands of your app's CLI. The app gives scripts
its own API; a script declares its arguments and flags and becomes a [cobra](https://github.com/spf13/cobra) command
with help, completion and strict parsing. Scripts run on [goja](https://github.com/dop251/goja), so users need
neither Node.js nor any other external program.

cliche holds only the mechanisms: finding scripts, reading declarations, building cobra commands, running scripts and
moving values and errors between Go and JavaScript. Everything about your domain stays in the app: the API module,
output format, error codes, exit codes, login, versioning.

## Install

```bash
go get github.com/hakastein/cliche@latest
```

Requires Go 1.26 or later.

## Quick start

A script `.demo/scripts/greet.js`:

```js
const app = require("app");

exports.definition = {
  short: "Greet someone",
  long: "Greet someone by name.",
  args: [{ name: "name", type: "string", usage: "who to greet" }],
  flags: [{ name: "shout", type: "bool", usage: "greet loudly" }],
};

exports.command = (name, flags) => {
  const text = "Hello, " + name + "!";
  return flags.shout ? app.upper(text) : text;
};
```

The app that runs it:

```go
config := cliche.Config{Name: "demo"} // .demo/scripts of the project and of the user's home
env := cliche.Env{Modules: map[string]any{
	"app": cliche.NewObject([]string{"upper"}, []any{
		cliche.Func(func(_ context.Context, args ...any) (any, error) {
			return strings.ToUpper(args[0].(string)), nil
		}),
	}),
}}

root := &cobra.Command{Use: "demo"}
cliche.Merge(config.Scan()).AddTo(root, func(cmd *cobra.Command, command *cliche.Command, in cliche.Input) error {
	value, err := cliche.Run(cmd.Context(), command, in, env)
	if err != nil {
		return err
	}
	fmt.Println(value) // printing the answer is the app's choice
	return nil
})
_ = root.Execute()
```

```console
$ demo greet world --shout
HELLO, WORLD!
```

The same program with an in-memory source runs as the package [example](example_test.go) under `go test`.

## Command scripts

- **Path is the command.** `docs/map.js` in a source is `demo docs map`; a directory holding commands is a group.
  A file without `exports.definition` is a library module that scripts of its source `require` by a relative path.
- **Sources.** `Config.Builtin` (scripts embedded in the app) comes first, then `Config.Sources`. By default these are
  the nearest `.<name>/scripts` at or above the working directory and `~/.<name>/scripts`. `Merge` keeps the command of
  the earlier root on a conflict and reports the other as a warning; an app with its own rule reads `Scan()` itself.
- **Declaration.** `exports.definition` is a plain literal read without running the module: `short`, `long`, `args`,
  `flags`. Arguments may be optional, multiple or groups of slots; flags may be multiple, have `choices` and a
  `default`. Base types are `string`, `path`, `int`, `bool`, `pair` (`Name=value`) and `duration`; the app adds its own
  through `Config.Types`. The app also registers keys of its own beside `short`, `long`, `args` and `flags` through
  `Config.Keys`: it checks their literal values itself and reads the result from `Command.Keys` after `Scan`. Any
  other key makes the declaration unreadable.
- **Call.** `exports.command` gets the arguments in declared order and the flags object last. A call the declaration
  does not take fails with `*UsageError` before the script runs.
- **Runtime.** CommonJS in strict mode with `async`/`await` and promises, a fresh runtime per run; cancelling the
  context stops the script. There is no sandbox: a script is trusted like the repository it lives in.

## Bridge between Go and JavaScript

The app gives scripts values through `Env.Modules`: `nil`, `bool`, `string`, `int64`, `float64`, `*big.Int`,
`[]byte`, `[]any`, `*Object`, its own `Map` and `List`, `Func`, `Async`, `*Promise` and `Getter`. Values from Go are
read-only in JavaScript. A script returns the same kinds of values back to Go.

`Env.Node` opts into a synchronous subset of Node.js: `fs` (`readFileSync`, `writeFileSync`, `mkdirSync`,
`existsSync`, `readdirSync`, `statSync`), `Buffer`, and `fetch` for `GET` requests.

## Async functions and commands

An `Async` is a `Func` that runs in a goroutine of its own: the script gets a promise, and the value or the error
crosses the bridge as for a `Func`. A command may be `async`: `Run` runs the loop until its promise settles and
returns the value; a rejected promise is an error of the command, as an uncaught exception is. Run them in parallel
with plain JavaScript:

```js
const app = require("app");

exports.definition = { short: "Fetch both", long: "Fetch both pages at once." };

exports.command = async () => {
  const [a, b] = await Promise.all([app.page("a"), app.page("b")]);
  return a + b;
};
```

```go
"page": cliche.Async(func(ctx context.Context, args ...any) (any, error) {
	return load(ctx, args[0].(string)) // minutes of work; the script awaits it
}),
```

- **Events of running work.** A `*Promise` is a promise the app settles from any goroutine with
  `Settle(value, err)`; the first settlement counts. A `Func` starts work in a goroutine of the app and returns
  promises of its events and of its result, so the script can `await` an event before the result:

  ```go
  "run": cliche.Func(func(ctx context.Context, args ...any) (any, error) {
  	started, result := new(cliche.Promise), new(cliche.Promise)
  	go func() {
  		result.Settle(work(ctx, func() { started.Settle(cliche.Undefined{}, nil) }))
  	}()
  	return cliche.NewObject([]string{"started", "result"}, []any{started, result}), nil
  }),
  ```

  ```js
  const run = app.run();
  await run.started;
  return await run.result;
  ```
- **Cancellation.** The `ctx` of an `Async` is the context of `Run`: cancelling it stops the script and the loop with
  `*InterruptedError`. `Run` cancels work the command did not wait for and returns only after every `Async` goroutine
  has ended, so an `Async` returns once `ctx` is done.
- **A hung script.** While the command promise is pending, `Run` waits for running `Async` calls and pending
  `*Promise` values the script holds, until `ctx` is done. A command promise with neither left to settle it is a
  `*ScriptError`. When `Run` returns, a pending `*Promise` no longer refers to the run.

## Errors

An error of a `Func` reaches the script as a read-only `Error`; its fields come from the error's `ScriptFields()`.
An app error the script leaves uncaught comes back from `Run` as the same Go error. Faults of cliche are types:
`*ScriptError`, `*UsageError`, `*FileError`, `*FetchError`, `*InterruptedError`. Error codes, stderr format and exit
codes are up to the app.

## Documentation

- API reference: [pkg.go.dev](https://pkg.go.dev/github.com/hakastein/cliche).
- Full rules of declarations, sources, the bridge and errors:
  [ADR-0001](docs/adr/0001-the-framework-is-mechanisms.md); promises, the loop and cancellation:
  [ADR-0002](docs/adr/0002-promises-and-the-loop.md) (in Russian).
- Glossary: [CONTEXT.md](CONTEXT.md) (in Russian).
- Changes: [CHANGELOG.md](CHANGELOG.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
