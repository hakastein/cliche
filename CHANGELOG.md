# Changelog

All notable changes are listed here. Versions follow [Semantic Versioning](https://semver.org/).

## v0.2.0 — Unreleased

- App keys of a declaration: `Config.Keys` registers keys beside `short`, `long`, `args` and `flags`, each with its
  own `Key.Read` and requiredness; the value is read as a pure literal without running the module and lands in
  `Command.Keys` after `Scan`. `Literal` and `LiteralObject` carry the place of every value, and
  `Literal.Errorf` makes a refusal a `*ScriptError` at that place. A key of cliche in `Config.Keys` panics in `Scan`.

## v0.1.1 — 2026-10-13

- MIT license.
- README in English, contributing guide, changelog and a runnable package example.

## v0.1.0 — 2026-10-13

- Script sources and catalog: `Config`, `Dir`, `FS`, `DefaultSources`, `Merge`, `Catalog.AddTo`.
- Declarations read without running the module; cobra commands with help, completion and strict parsing.
- goja engine with CommonJS `require`, the Go↔JS bridge and the Node subset (`fs`, `Buffer`, `fetch`).
- Error types `ScriptError`, `UsageError`, `FileError`, `FetchError`, `InterruptedError`.
