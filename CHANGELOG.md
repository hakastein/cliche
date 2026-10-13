# Changelog

All notable changes are listed here. Versions follow [Semantic Versioning](https://semver.org/).

## Unreleased

- MIT license.

## v0.1.0 — 2026-10-13

- Script sources and catalog: `Config`, `Dir`, `FS`, `DefaultSources`, `Merge`, `Catalog.AddTo`.
- Declarations read without running the module; cobra commands with help, completion and strict parsing.
- goja engine with CommonJS `require`, the Go↔JS bridge and the Node subset (`fs`, `Buffer`, `fetch`).
- Error types `ScriptError`, `UsageError`, `FileError`, `FetchError`, `InterruptedError`.
