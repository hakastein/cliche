# Changelog

All notable changes are listed here. Versions follow [Semantic Versioning](https://semver.org/).

## v0.1.1 — 2026-10-13

- MIT license.
- README in English, contributing guide, changelog and a runnable package example.

## v0.1.0 — 2026-10-13

- Script sources and catalog: `Config`, `Dir`, `FS`, `DefaultSources`, `Merge`, `Catalog.AddTo`.
- Declarations read without running the module; cobra commands with help, completion and strict parsing.
- goja engine with CommonJS `require`, the Go↔JS bridge and the Node subset (`fs`, `Buffer`, `fetch`).
- Error types `ScriptError`, `UsageError`, `FileError`, `FetchError`, `InterruptedError`.
