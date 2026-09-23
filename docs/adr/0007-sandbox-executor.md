# ADR 0007 — Sandboxed execution of actions: one sandbox per process, one provisioner per environment

**Status**: accepted · **Date**: 2026-09

## Context
`script` actions execute code entered by methodologists (JavaScript, Go). It must never run in the
engine, nor be able to reach the host, the internal network, or secrets. The platform deploys
on Docker Compose (dev), Kubernetes, and potentially bare metal.

## Options
1. Interpreters in the engine ("language" sandbox only) — simple, but an interpreter
   flaw compromises the engine and its secrets.
2. A container **per action** — strong isolation but startup cost (~1 s) on every action.
3. A sandbox **per process** reused for its actions, behind a provisioner abstraction.

## Decision
Option 3:
- `engine.Sandboxes` (Acquire / Release per process) implemented by `sandbox.Pool` on a
  `Provisioner` (`process`, `docker`, `kubernetes`; `inproc` for tests) chosen via `GOAP_SANDBOX`.
- The sandbox runs `goap-runner` (SandboxService). Operations leaving the script go through the
  engine's RuntimeService with a **per-job token**: the sandbox has no identity, no secret, and no access
  to the graph or the model gateway.
- Defense in depth: interpreters without files / network / processes + container or
  pod isolation (read-only rootfs, no capabilities, non-root, limits, internal network, optional gVisor).
- Sandboxes stopped at the end of the process or after inactivity.

## Consequences
- The engine drives infrastructure (Docker API via a restricted proxy, or `pods` rights limited to a
  dedicated namespace on Kubernetes).
- Latency: sandbox startup on a process's first script; outgoing DSL calls make a round trip
  over the network to the engine.
- An infinite loop in an interpreted Go script cannot be interrupted: the job fails on timeout and
  the (disposable) sandbox is recycled along with the process.
