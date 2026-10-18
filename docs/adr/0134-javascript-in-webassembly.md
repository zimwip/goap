# ADR 0134 — JavaScript in WebAssembly, compiled at save; Go scripts and yaegi removed

**Status**: accepted, implemented (first step) · **Date**: 2026-10 · Builds on ADR 0018 (algorithms), 0019 (adapters),
0062 (builtins and sandboxes)

## Context

The scripts of goap (script actions, the algorithms of the domains, the adapters) ran in two interpreters inside the Go
process: goja for JavaScript and yaegi for Go. Both re-parsed the source at every run, and neither can bound the memory
a script takes; yaegi cannot even stop a script at its deadline (the goroutine is left running). The property and node
validators run in the commit transaction, once per node of their type, so the cost of a run is on a hot path.

We want a compiled form of the scripts, produced when they are saved, and a sandbox that holds by construction. The
question was whether the tool chain can live in the backend itself, with no external compiler and no cgo (the
platform builds for Windows too).

What can be embedded:

- **JavaScript**: QuickJS compiled to WebAssembly is itself a compiler of JavaScript to QuickJS bytecode.
  `github.com/fastschema/qjs` ships it (a 1 MB `qjs.wasm`, `go:embed`) and runs it with wazero, a WebAssembly runtime
  written in Go. The whole chain, compiler included, is in the binary.
- **Rust, Go, AssemblyScript** to WebAssembly need their compilers (rustc, the Go tool chain, TinyGo, Node for
  AssemblyScript): none of them can be embedded in the backend. A module compiled elsewhere can be run by wazero, which
  is a later step (below).

## Decision

### 1. JavaScript is the only script language

`go` is no longer a script language: `algo.JavaScript` is the only one (`algo.Go`, the yaegi interpreter, the
`dsl.Symbols` export and the `func Run(ctx *dsl.<Usage>Ctx) error` convention are gone). The two shipped Go scripts are
rewritten in JavaScript: `not-blank` of `alm` (1.7.1) and `design_tests` of `test-design` (1.3.1). The web offers
JavaScript only.

### 2. Two engines behind one seam

`lib/dsl` runs JavaScript through `jsEngine` (`compile`, `run`), chosen per process by `GOAP_JS_ENGINE`:

- `goja` (default): in the Go process, a VM per run.
- `wasm`: QuickJS compiled to WebAssembly, run by wazero (`lib/dsl/wasmengine.go`). The runtimes are pooled; the
  script sees `ctx` with the methods of the context object in lower camel case, `console`, no `std` / `os` module and
  an empty directory as its only file system. Its memory is capped (256 MB) and a script at its deadline is stopped for
  real (the module is closed, the runtime dropped). The values cross between Go and the script as JSON (a Go struct is
  an object named by its json tags, a number a `float64`); the code runs in strict mode; an exception is caught inside
  the script's wrapper and never crosses into qjs (whose exception path does not leave the runtime sound). A runtime
  whose global object a script changed, or that ran a failing script, is not reused.

Both engines compile a script once and keep the compiled form by the hash of its source (`compiledCache`):
`CheckAlgorithmCode`, called when a domain is validated (at save and publication), compiles it, so the runs take the
compiled form. The QuickJS module itself is compiled to native code by wazero once per process, cached on disk in the
user cache directory (`goap/qjs`) between starts.

### 3. Every test passes on both

`make test-wasm` (`.\make.ps1 test-wasm`) runs every Go test with `GOAP_JS_ENGINE=wasm`; `lib/dsl` `TestWasmEngine`
runs the DSL tests on the wasm engine within `make test` too. wazero stays at the version qjs is built against
(v1.9.0): a later wazero refuses the module of qjs 0.0.6.

## Measures

`BenchmarkValidator` (`lib/dsl`), one run of a property validator, warm: goja ≈ 25 µs, wasm ≈ 250 µs. The wasm engine
is slower on goap's scripts, which are short glue around `ctx`: each crossing between Go and the module costs tens of
microseconds, where goja calls Go directly. QuickJS is faster than goja on computing (its own benchmarks), which goap's
scripts hardly do. Cold start of the wasm engine: ≈ 0.6 s to compile the QuickJS module, then taken from the disk cache.

## Consequences

- goja stays the default: the wasm engine is the isolated one (memory, deadline), not the fast one. Choosing it is a
  matter of isolation until its crossing cost is worked on.
- The compiled form lives in memory, per process. Storing the bytecode with the domain version (so a graph service
  never compiles) is a possible next step; it would tie the stored form to the version of QuickJS.
- Next steps: a `wasm` script language running a module compiled elsewhere (Rust, TinyGo, AssemblyScript), with a
  host interface for `ctx` (WIT or a JSON call like the one of §2), for the algorithms that compute; fewer crossings
  per run (one call carrying the inputs the script reads).
