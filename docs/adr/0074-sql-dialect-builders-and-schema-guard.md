# ADR 0074 — One statement per query, a guard on the two schemas

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0010, 0054.

## Context

The graph has two SQL repositories behind one `Tx` interface: PostgreSQL (`postgres.go`, pgx, `$n`, uuid / jsonb /
`int32[]` / `COPY`) and SQLite for the local mode (`sqlite.go`, `database/sql`, `?`, text encodings). Their statements
were written twice and differed only by placeholders, a `::text` cast, `IS DISTINCT FROM`, the `true` literal and an
order tie-break; the schemas (`migrations/0001_schema.sql`, `migrations_sqlite/0001_schema.sql`) are twins by hand, as
are those of five other services. Nothing said when a twin drifted, and the PostgreSQL code ran only when
`GOAP_TEST_PG_DSN` was set (no CI configuration exists).

## Decision

- **Shared statements, separate executors.** `pkg/graph/sqlbuild.go` builds the text and the arguments of the queries
  both repositories run from a `dialect` value (`dialectPG`, `dialectSQLite`): the placeholder (`$n` / `?n`, both bind
  by number, so a value used twice is one argument), `id` / `idOr` (a uuid column read as text), `differs` (`IS
  DISTINCT FROM` or the explicit null test), `where1`, `orderBy`. Built: node by version / latest on a branch /
  versions and joined branches / by key / in a baseline, the baseline entries, out and in links, branches, branch
  joins, baseline and change id lists, change impacts, the log and its counts (`logWhere` already took the
  placeholder), tags, the plain inserts, the node origin / properties updates and the whole of `DeleteChange`. The
  executors, the scanning, the encodings (`jsonb`, `tsText`, arrays, `COPY`), the upserts (`ON CONFLICT ... SET`),
  `LatestNodes` (`DISTINCT ON` against a `max(version)` subquery) and the SQLite `change_impact.seq` assignment stay in
  their repository: they do not share text. The driver does not change and there is no code generation.
- **A golden test per query and dialect** (`sqlbuild_test.go`): the text the repositories ran before the builders,
  whitespace collapsed; SQLite placeholders are compared after normalising `?` / `?n`, since the builders number them.
- **A guard on the schemas** (`internal/sqlschematest`, test-only): it reads the migrations of a dialect in file order
  (`CREATE TABLE`, `CREATE INDEX`, `ALTER TABLE ... ADD FOREIGN KEY` / `RENAME` / `DROP`, `DROP TABLE`), reduces them
  to a common vocabulary (uuid / timestamptz / jsonb / `integer[]` are text, boolean and serial types are integers, a
  boolean default is 0 / 1, `DEFAULT now()` is ignored because the repositories write the time, a serial that is not a
  primary key is not compared as generated, `WHERE NOT flag` is `flag = 0`, an inline and an `ALTER` foreign key are
  one) and lists every difference of tables, columns, nullability, defaults, primary keys, unique and check
  constraints, foreign keys and indexes. `TestSchemasAligned` runs it for the graph, `credsvc`, `mcpsvc`, `modelgw`,
  `prefssvc` and `registrysvc`; `pkg/index` is not covered (pgvector, `tsvector` and generated columns on one side,
  FTS5 virtual tables on the other). The other schema is not generated from the first: the guard is the deliverable.
- **`make test-pg`** runs every test against PostgreSQL: `GOAP_TEST_PG_DSN` when set, else a throw-away
  `pgvector/pgvector:pg17` container (the image of the compose file). There is no CI configuration in the repository;
  the target is what to run before touching `postgres.go` or a `migrations/` file.

## Consequences

A query changes in one place; a new one goes in `sqlbuild.go` with its golden text. A schema edit that reaches one
dialect only fails a test. What still differs, by nature, between `postgres.go` and `sqlite.go`: the executors and the
scanning (pgx rows against `database/sql`, `RowsAffected`), the value encodings, bulk insertion (`COPY` against a
prepared statement), the upserts, `LatestNodes`, `PutChangeImpact` and `AppendLog`'s `RETURNING seq` against
`LastInsertId`.
