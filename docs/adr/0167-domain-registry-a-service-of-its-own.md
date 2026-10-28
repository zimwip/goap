# ADR 0167 — The domain registry, a service of its own, holds the built-in domains protected

**Status**: accepted, implemented (Go and Rust) · **Date**: 2026-10 · Amends ADR 0125 §3 (the domain registry served
by the graph), ADR 0012 / 0013 (the built-in domains compiled in and frozen), ADR 0113 (the shipped domains seeded by
the graph).

## Context

The domains are the definition of every graph: node types, link types, lifecycles, algorithms. Since ADR 0125 §3 their
registry (`graph/domainsvc`) was served by the graph process, on the graph's database, and the three built-in domains
(`methodology`, `organisation`, `platform`) were not in it at all: they were compiled into every binary
(`lib/domains/builtin`, embedded; Rust `include_str!`), listed first by `ListDomains`, flagged `builtin` and refused
every write ("built into the platform"). Every reader of a catalogue (`typecat.New`, Rust `Catalog::new`) added them
from the binary.

Two things did not hold. The built-in domains are the platform's own, not a graph's: a graph should rely on the
registry only, whichever domain it is. And "frozen" was a property of the code, not of the registry: a platform that
wants to ship a new version of `organisation`, or freeze a domain of its own, had no way to say so.

## Decision

1. **The domain registry is a service of its own** (`platform/cmd/domains` in Go, `goap-domains-rs` in Rust, the same
   contract, `registry.v1.DomainService`, port 8096), on a database of its own (PostgreSQL role and schema `domains`;
   `domains.db` in the local stack): `graph/domainsvc` keeps the store, the migrations and the service; the graph
   process no longer migrates, seeds or serves it. Its events go through its outbox (`domain_outbox`) to NATS, as
   before (`goap.registry.domain.saved|published|deleted`). Who may write a domain stays the organisation's rules
   (ADR 0020): the registry reads the organisation from the platform graph (`GOAP_GRAPH_URL`) when a person asks.
2. **The built-in domains are stored domains**, seeded at each start of the registry from files
   (`GOAP_BUILTIN_DOMAINS_DIR`, else `<GOAP_DOMAINS_DIR>/builtin`, else `lib/domains/builtin`: `BuiltinDir`; the
   images copy them to `/domains/builtin`), published when their version is not stored yet (they reference each other:
   a file is tried again once another landed, `SeedBuiltinDomains`), then the shipped domains of `GOAP_DOMAINS_DIR`
   (ADR 0113). `def.Record.Builtin` and the proto `builtin` fields are gone; the catalogue of a service is exactly the
   domains in force (`typecat.Of`, Rust `Catalog::of`); `typecat.New` / `Catalog::new` still add the files' built-in
   domains for the files and the tests. A methodology never pins a built-in domain (ADR 0113): `pinDomains` and
   `domainsInForceAt` skip them by name (`def.IsBuiltinDomain`, Rust `BUILTIN_NAMES`).
3. **A domain may be protected** (table `domain_protection`, migration 0003 in both dialects; `Store.Protected` /
   `SetProtected`; proto `Domain.protected`, `DomainSummary.protected`): a protected domain is refused every write over
   RPC (save, publish, new version, delete, import; `ErrImmutable`, `FailedPrecondition`, "domain X is protected: it is
   written by the local command line only"). The seeds protect the built-in domains. Only the registry's command line,
   run against its database, writes a protected domain or changes the protection (`domainsvc.Local(ctx)` marks such a
   context, never set by the handler): `domains publish <file>...`, `protect <name>...`, `unprotect <name>...`,
   `list` (the same commands for `goap-domains-rs`). The lock is the registry's alone: a graph knows nothing of it. The
   web reads `protected` (read-only drafts and editors, "protected" in the explorer).
4. **A graph is a client of the registry** (`GOAP_DOMAINS_URL`, default `http://localhost:8096`): it waits at its start
   until the registry holds the built-in domains (`loadTypes`, Rust `Registry::wait_for_builtins`), builds its catalogue
   from the domains in force (`typecat.NewLive` over `domainclient`, Rust `platform::Registry`), then reads them again
   on the registry's events (`goap.registry.domain.published|deleted`, and `service.ready.domains` for a registry
   started again; with no bus, every minute). The requests read the catalogue kept meanwhile, never the registry: a
   registry gone or slow leaves the graph serving with the domains it read. A typed graph asks only its
   catalogue for structures, required links and `adminOnly` (`graphapi.ValidatorQuery.Structures`): no fallback to the
   compiled-in domains; the untyped graph of the tests keeps `typecat.Builtin()`. The Rust sessions carry the catalogue
   (`Session::with_catalog`, `ReadTx::catalog`, `access::catalog_of`). The content graph reads its domains from the
   registry too, no longer from the platform graph. The engine, the methodology registry and the hub read it at
   `GOAP_DOMAINS_URL`; the gateway routes `DomainService` to it.

## Consequences

- Compose has a `domains` service (the Rust image), started before the graphs; the performance stack runs the Go one.
  `goap-local` starts it first (`goap-domains-rs` with the Rust graph, else the Go `domains`) and restarts it on a
  change of `domains/` or `lib/domains/builtin`. The new PostgreSQL role needs a fresh volume (`make reset`).
- A new version of a built-in domain ships as a file: the registry publishes it at its next start, protection kept.
  Changing a built-in domain by hand is the command line's, on the registry's database.
- Nothing compiles the built-in domains into a service any more; the files are read by the registry, the tests and the
  tools only (Rust `graph_catalog::catalog::builtin_dir`).
