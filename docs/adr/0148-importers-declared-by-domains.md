# ADR 0148 — Importers declared by domains, implemented by services

**Status**: accepted; implemented · **Date**: 2026-10

## Context

ADR 0147 loads a git repository into the `code` domain, but only from a command line (`goap-gitimport`): a person in
the IDE has no way to run it, and the next source (a ticket tracker, a CMDB export, a spreadsheet of requirements)
would be one more command, or code added to the graph service. A source belongs to the domain it fills (the git
import makes sense only for `code`), while its code (git, a tracker's API, a parser) has nothing to do in the platform.
This is the split the connectors already make for tools (ADR 0028): the platform states what is expected, a separate
service implementing it registers itself.

## Decision

1. **A domain declares its sources.** `importers: [{name, label, description, params}]` on a domain (`def.Importer`,
   `lib/def/importer.go`; proto `registry.v1.Domain.importers`, `ImporterDecl`); `params` are attributes, checked like
   any (types, enums of the domain, defaults). `code` 1.1.0 declares `git` (`repository`, `name`, `rev`, `base`, `max`,
   `symbols`). The declaration is versioned with the domain and published like it.
2. **A service implements them.** An importer service implements `importer.v1.ImporterService` (`Describe`, `Start`,
   `Jobs`, `Cancel`) and says which `(domain, importer)` pairs it implements. `lib/importerkit` is the whole kit: a
   service implements `importerkit.Importer` (`Info`, `Run(ctx, *Job)`) and calls `importerkit.Run(name, im)`, which
   serves the protocol, keeps the jobs (in memory, the last `KeepFinished` finished ones), registers with the registry
   and renews its lease. The first one is `importer-git` (`platform/cmd/importer-git`, `gitimport.Plugin`): a local
   path under `GOAP_IMPORT_GIT_ROOTS`, or a remote URL cloned bare under `GOAP_IMPORT_GIT_CLONES` and fetched again on
   the next run (`file://` refused).
3. **The import registry** (`graph/importsvc`, `importer.v1.ImportService`, served by the graph next to the domain
   registry, routed by the gateway): `RegisterImporter` (the shared token `GOAP_IMPORTER_TOKEN`, header
   `X-Goap-Importer-Token`; no token, no registration; a lease of `DefaultLease`), `ListImporters` (the importers the
   domains in force declare, each `available` when a live service implements it; an implemented pair no domain declares
   is answered as `undeclared` and not offered), `StartImport`, `ListImportJobs`, `CancelImportJob`. The registry checks
   the parameters against the declaration (unknown, type, enum; defaults filled) before starting, so a service receives
   only what its domain declares. `goap-dev` adds the git importer in process (`Service.AddLocal`, no token, no lease).
4. **Who may run one**: `import:run` on `authz.Resource{Type: "import", Namespace, Name, ProjectID}` (deny by default,
   ADR 0138: administrators and project administrators by the default policies). The project is the caller's active one
   unless named. Jobs are listed to who started them, all to an administrator; who started a job may cancel it.
5. **A run is an ordinary producer.** The service writes through the graph's change operations (`graphclient`) as
   `system:<service id>` with the `admin` role, on behalf of the person who started it (`Principal.For`, ADR 0114):
   every change it makes is reviewable, journaled and attributed like any other. The git importer's changes are the
   ones of ADR 0147, and it resumes where the repository node says it stopped.
6. **Web**: the Baseline explorer's *Import from a source* button opens the `sources` tab of the namespace
   (`SourceImportTab.svelte`, `web/src/lib/importSources.ts`): the declared sources, the form generated from their
   parameters, and the runs, followed by polling while one runs (the importer services publish no event).

## Consequences

- Adding a source is two things, neither in the platform: declare it in its domain, deploy a service built on
  `importerkit` (compose: one service with its token).
- Jobs live in the importer service: restarting it forgets them (the imports themselves are on the graph, and the git
  importer resumes). No event is published for a job; a durable job store and events can come later.
- The domain form of the web carries `importers` as is: no editor for them yet (YAML / JSON import of the domain).
- The git importer's symbols need cgo (Tree-sitter); the image built `CGO_ENABLED=0` imports without them.
