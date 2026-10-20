# ADR 0147 — Git repositories in the graph: one commit, one change

**Status**: accepted; implemented · **Date**: 2026-10

## Context

The graph versions what an enterprise changes, and much of it is source code kept in git. Loading a repository with
its history gives the graph a large, real, well understood corpus: every commit says who changed what and why, the
trees say what a directory holds at each point. The target is the Linux kernel (a million commits on its first-parent
history, about 90 000 files today), so the import must stream the history and must not hold it.

## Decision

1. **A `code` domain** (`domains/code.yaml`, seeded with the other shipped domains): `Entry` (`name`, `path`, lifecycle
   `presence`), its subtypes `Directory` (`tree`: the git tree object), `File` (`blob`, `mode`, `size`, `extension`;
   a symbolic link and a submodule are files of their mode) and `Repository`, the root directory (`url`, `ref`,
   `commit`: the last commit imported). A directory holds its entries by `contains` links (`compose: true`). Keys are
   `<repo>` for the root, `<repo>:<path>/` for a directory and `<repo>:<path>` for a file: several repositories share the
   namespace, and a path that is a file at one commit and a directory at another is two nodes.
2. **One commit is one change**, on the first-parent history of the revision followed (a merge brings the merged work in
   as one commit, as `git log --first-parent` shows it): the change's title is the subject, its intent the message, its
   `Data.git` the commit (id, parents, tree, author, committer and their dates). The change goes through the operations
   every change uses: `ImpactNodeCreate` for a new entry, `ImpactNodeCheckout` + `ImpactNodeUpdate` and link edits for a
   changed one, `ImpactNodeTransition` to `removed` for a removed one, one review batch accepting every impact, `Apply`.
3. **A version of a directory is a git tree.** git names the trees a commit changes (`diff-tree -r -t`), so a changed
   file versions every directory up to the root, and each directory's draft keeps its links, which the landing points
   at the versions it writes (ADR 0003, ADR 0079): the links of a directory always name the versions of its entries at
   the head, never a suspect version, and reading the tree from the repository node is reading the commit.
4. **A node is never deleted** (ADR 0077): a removed entry leaves its directory and moves to `removed` (a removed
   directory drops its links too); added again at the same path, it is the same node, `restore`d.
5. **Streaming, resumable.** One `git log --first-parent --reverse --raw -t -z` process for the whole history, read as
   it writes, one `git cat-file --batch-check` for the sizes; the importer keeps only the repository at the head (key →
   node, links), read whole once and followed by `BaselineDelta` after each landing (the whole head past
   `MaxDeltaNodes`). The repository node names the last commit imported: a run resumes there (`-max` bounds a run). A
   first import may start at a revision (`-base v6.12`): its tree is the first change, then the commits after it.
6. **Symbols, read by Tree-sitter** (`-symbols`, `gitimport.TreeSitter`, C, C++, Go, Python, Java, JavaScript,
   TypeScript, Rust): each declaration of a source file a commit adds or changes (function, method, prototype, struct,
   union, class, interface, enum, type, namespace, module, import, top-level variable, constant, field, macro; the
   declarations local to a function are left out) is a `Symbol` node (`name`, `kind`, `container`, `language`,
   `signature`, `digest`) the file `defines` (inverse label "defined in"), keyed `<file key>#<kind>:<qualified name>`
   (`~2`, `~3` for a repeated one). The link goes from the file: a version of a file carries its links forward, so an
   unchanged symbol keeps its version and its link stays valid, while a `defined_in` link on each symbol would have to
   version every symbol of a file at each of its commits. A symbol has no line numbers: its versions are the changes of
   its own text (`digest`), not of the lines around it. The queries follow the tags convention of Tree-sitter
   (`@definition.<kind>`, `@name`), one per language in `platform/gitimport/treesitter.go`. Tree-sitter is C: it builds
   with cgo only (`treesitter_nocgo.go` refuses `-symbols` in the images, built with `CGO_ENABLED=0`).
7. **Through the graph's API** (`gitimport.Port`, implemented by `*graph.Graph` and `graphclient.Client`): the command
   `goap-gitimport` talks to the graph service or `goap-dev` as a system principal (or with a token, through the
   gateway); the importer is a producer like any other and needs no access to the graph's storage.

## Consequences

- Any git repository loads with `goap-gitimport -repo <dir> -name <name>`; the tests write repositories covering
  additions, modifications, removals, a file becoming a directory, a merge, an empty commit, a resume, a base and the
  symbols of a file across its commits.
- The cost of an import is the cost of the graph's change operations, one transaction each. Measured on SQLite (in
  process, `TestImportRepository` with `GOAP_GITIMPORT_REPO`): a first commit of 1 600 files takes about 10 s, and the
  cost per operation grows with the size of the change (the operations fold the change's impacts, `ImpactsSeenBy`,
  `resolve`, and the review checks each accepted draft), so a large first change (the 17 000 files of Linux 2.6.12, or
  a recent kernel tree as a base) is where the graph's change path has to scale; the commits after it touch a few files
  each. The importer is a ready load for that work.
- With the symbols, a commit writes as many nodes as declarations it changes: jq's first 300 commits ran at about
  4.5 commits/s with them (5 500 symbol versions), against 20 without.
- The declarations by type, their links and the packages: ADR 0150.
- Not done: renames are an addition and a removal (no rename detection, a file's history does not follow it), the
  contents of the files are not read (only their object ids and sizes), branches other than the one followed and tags
  are not imported.
