# ADR 0149 — A job service for the long runs, followed over SSE

**Status**: accepted; implemented · **Date**: 2026-10

## Context

Three things a person starts from the IDE run longer than a request should: an import from a source (ADR 0148, a git
repository loaded commit by commit), the import of a graph snapshot (ADR 0104) and the export of a namespace (JSON,
ADR 0104, or OKF, ADR 0111). Each did it its own way: the importer services kept their jobs in memory and the web
polled them every two seconds; the snapshot import held the request open until the change was written; the OKF export
streamed its zip on a Connect stream the browser gathered in memory, and the JSON export answered the document in one
response. None said how far it was, none survived the tab being closed, and the next long run (a reindex, a bulk
review, a report) would have invented a fourth way.

## Decision

1. **One job service** (`platform/jobsvc`, `platform/cmd/jobs`, Connect `goap.jobs.v1.JobService`, port 8091). A job is
   a record: `kind`, `label`, `target` (what it works on, a namespace for the ones here), `params`, the person it
   runs for (`by`, project, unit), its status (`queued` → `running` → `done` | `failed` | `cancelled`), its progress (a
   line, `done` / `total`, counters `stats`), its `output` (a small JSON answer: the change an import created) and its
   `result` (a file: the export). The service keeps them in its own database (`job`, `job_blob`, both dialects,
   ADR 0074) for `GOAP_JOB_RETENTION` (7 days) after they end, and emits `goap.job.<id>.created` / `started` /
   `ended` through its outbox (`job_outbox`, ADR 0136); the progress itself is not an event of the bus.
2. **A job is started by the service owning its kind, for the person who asked.** That service authorizes the run and
   checks its parameters, then creates the job (`CreateJob`, with the person's principal: the run acts as them). The
   job service never interprets a kind. Here: the import registry (`StartImport`: `import.<domain>.<importer>`, the
   permission and the parameters of ADR 0148), the graph (`StartGraphExport`: `graph.export`, format `json` | `okf`,
   read like `ExportGraph`; `StartGraphImport`: `graph.import`, the snapshot as the job's input, each type gated like
   `ImportGraph`).
3. **Workers pull.** A worker is any process running kinds: it claims the oldest queued job of its kinds
   (`ClaimJob`, long-polled), reports its progress (`ReportJob`, which renews a lease of `DefaultLease`, 30 s, and
   answers whether a cancel was asked), reads its input (`ReadJobInput`) and writes its result in chunks
   (`WriteJobResult`). A running job whose lease lapses fails ("worker lost"); a queued one waits for a worker. The
   workers' calls and `CreateJob` need the services' token (`GOAP_JOB_TOKEN`, header `X-Goap-Job-Token`); the people's
   calls act for the caller. `lib/jobkit` is the whole kit: `jobkit.Queue` (the service in process, `jobkit.Client`
   over RPC), `jobkit.Worker{Kinds: map[kind]Runner}` and `*jobkit.Job` (the parameters, `Progress`, `Step(done,
   total)`, `Set`, `Output`, `Input`, `Result`; an `io.Writer` of progress lines). `ListWorkers` answers the kinds a
   live worker claims: the import registry tells from it which importers are `available` (no registration of its own
   any more: `RegisterImporter`, `importer.v1.ImporterService` and `GOAP_IMPORTER_TOKEN` are gone). An importer
   service is a worker of the kinds of the importers it implements (`importerkit.Run`, unchanged for its author); the
   graph process is the worker of `graph.*` (`graphsvc.JobRunners`).

   **Feedback.** Every progress line a run says (`Job.Progress`, or a line written to the job) is also a line of its
   log (`jobkit.LogLine{At, Text}`): a report carries the lines said since the previous one (taken back when the report
   fails), the service keeps the last `KeepLog` (100) on the job (`Job.log`, column `log`), so the people read what the
   run did, not only where it is. `Step(done, total)` gives the progress bar and, with the start, the time left. The git
   import says its phases (reading what the graph holds, counting the commits: `git rev-list --count --first-parent`),
   then each commit as it starts it ("commit 12/340 <hash> <subject>", done of total), its counters after each one, and
   a last line ("<name>: 12 commit(s) imported, up to <hash>", or up to date).
4. **People read their jobs.** `ListJobs` (own jobs; `all` for an administrator), `GetJob`, `CancelJob` (who started
   it, or an administrator: a queued job is cancelled at once, a running one when its worker next reports). Two HTTP
   endpoints, routed by the gateway under `/api/jobs/`: `GET /api/jobs/events` is a Server-Sent Events stream of the
   caller's jobs (a `snapshot` event with the recent ones, then a `job` event at every change, progress included;
   `: ping` every 15 s; `?job=<id>` for one job), and `GET /api/jobs/<id>/result` downloads the result (who started
   the job, or an administrator). The stream is fed by the service's own writes: the service runs as one replica.
5. **Web**: one stream (`web/src/lib/stores/jobs.svelte.ts`, an SSE reader over `fetch` since `EventSource` sends no
   `Authorization`, reconnecting with a backoff; pure reading in `web/src/lib/jobView.ts`, one row
   `components/JobRow.svelte`, a click on it opening the detail `components/JobDialog.svelte`: facts, parameters,
   counters, the whole log, cancel, download, the change), a *Jobs* indicator in the status bar (the jobs going and their overall progress; its
   list shows the recent ones and opens the `jobs` tab: every job, its progress bar, cancel, download, the change it
   created; an administrator may list every person's), a toast when a job ends, and the three runs on it: *Import from a source* opens the detail of the run it
   starts (its runs are followed in the Jobs, the tab lists none), the snapshot import of the Import tab opens the detail of its job and, once done, links
   the change, the exports of the Baseline
   explorer and tab download the result when the job ends (and the Jobs tab keeps it to download again). The
   streaming `ExportGraphOkf` and the web's chunk collector are gone; `ExportGraph` and `ImportGraph` stay for the
   API clients that want an answer in the request.

## Consequences

- A new long run is a kind: its owning service creates the job, a worker (that service or another) runs it with
  `jobkit`, and the web reads it from the same stream with nothing more than a label.
- Jobs outlive the tab and the importer service: a person reloads and finds their runs, and the export they left
  is still downloadable for the retention. A worker restarting fails what it was running (the git import resumes on
  the next run); a job service restarting keeps the records and fails the running ones whose lease lapses.
- One replica of the job service: the SSE stream follows the writes of its process. Several replicas would feed it from
  the bus (the outbox events) instead.
- The imports and exports of definitions (a domain, a methodology) stay synchronous: they are one validated write.
