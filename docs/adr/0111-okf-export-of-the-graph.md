# ADR 0111 — OKF export of the graph: one Markdown file per node

**Status**: accepted, implemented · **Date**: 2026-10

## Context

1. The graph of a namespace is exported as a JSON snapshot at a baseline (ADR 0104): one document, made to be imported
   back, not to be read, browsed or fed to tools working on files (a knowledge base, a static site, an LLM retrieving
   documents).
2. A knowledge structure of the graph needs each node as a document of its own: its identity and properties in a
   structured header, its texts as prose, its links as links between files.

## Decision

### 1. The OKF tree

`pkg/okf` renders a `graph.Snapshot` (the state of one baseline, no history) as a tree of Markdown files with a YAML front
matter (format `goap.okf/v1`):

- `<namespace>/index.md`: front matter `{format, namespace, branch, baseline, exportedAt, nodes, types: {type: count}}`,
  then the nodes listed by type as links to their files.
- `<namespace>/<Type>/<key>.md`, one per node (`<Type>` the local name of its type, the key made a portable file name:
  any character but letters, digits, `.`, `_`, `-` becomes `-`, a collision in a folder takes a `-2` suffix):
  - front matter `{key, type, title, state, owner, namespace, baseline, properties, links: [{type, to, path,
    properties}]}`: `to` the key of the target, `path` its file relative to this one; `owner` the key of the owning unit;
  - body: `# <title>` (the `asName` attribute of the type, else `title` or `name`, else the key), one `## <label>`
    section per string attribute edited as a text area (`widget: textarea`; a type the catalogue does not know:
    `description`), these properties left out of the front matter; `## Links` (outgoing, as relative Markdown links)
    and `## Referenced by` (incoming, from the other nodes of the snapshot).

The front matter carries everything the JSON snapshot does for a node but the body attributes, which the body holds, so
the tree is a faithful image of the baseline. Links to another namespace are left out, as in the snapshot.

### 2. Choosing the state: a baseline; a streamed zip

The tree is the state of a baseline, chosen like the JSON export (`namespace`, `branch`, `baseline_id`). It is served
by a server-streaming RPC, `ExportGraphOkf`: `okf.Write` writes the zip into the response while it renders the files,
one at a time (the archive is never held whole on the server): a first message names the archive (`filename`
`graph-<namespace>-<time>.okf.zip`, `media_type` `application/zip`, `nodes`), the next ones carry its bytes in order
(`chunk`, 64 KiB). A streaming call passes none of the unary interceptors of the graph service, so the handler
identifies the caller itself (an anonymous one is `Unauthenticated`).

Web: `web/src/lib/okfExport.ts` (`downloadGraphOkf`, over `stream.ts`'s `serverStream`) gathers the chunks as they
arrive and saves them as one file. The Baseline workspace exports the baseline it shows (the head of the branch, or the
row picked in the history) with *OKF* and *JSON*; the Baseline explorer's branch menu has *Export head (OKF Markdown)*
next to the JSON one.

## Consequences

- An export only: the tree is not imported back (the JSON snapshot is the import format of ADR 0104).
- The names and texts come from the type catalogue of the graph (`okf.FromTypes`); an untyped graph gets the defaults.
