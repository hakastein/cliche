# Contributing

Bug reports and pull requests are welcome on [GitHub](https://github.com/hakastein/cliche).

## Before you start

- cliche is mechanisms only. Domain concepts (document types, error code vocabularies, exit codes, output formats,
  login, API versions) belong to the app and are not accepted into the framework; see
  [ADR-0001](docs/adr/0001-the-framework-is-mechanisms.md).
- Open an issue before a change of behaviour or of the public API. A change of a recorded decision updates its ADR in
  the same pull request.
- Design records ([`docs/adr/`](docs/adr/)) and the glossary ([`CONTEXT.md`](CONTEXT.md)) are in Russian.

## Development

```bash
make go   # gofmt check, go vet, go test -race
```

CI runs the same target on every push and pull request.

## Tests

- Tests live in the external package `cliche_test` and use testify with table cases; a test body has no logic.
- The seam is the execution of a command: a test builds a cobra root from in-memory sources and a stub API module,
  runs argv and checks the answer, the error and the API calls.
- Assert values, not prose: no checks of messages or help text. goja, cobra and pflag are not tested.
- Every guarantee of an ADR is held by a test that fails when the guarantee is removed.

## Pull requests

- One change per pull request, against `main`.
- Keep Go plain: the consumer declares an interface; no new concept for a hypothetical case.
- `make go` passes.

## Releases

A release is a `vX.Y.Z` tag on a commit of `main`, set by the maintainer after the merge, with an entry in
[CHANGELOG.md](CHANGELOG.md).
