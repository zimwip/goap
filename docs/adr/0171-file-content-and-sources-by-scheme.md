# ADR 0171 — The content of files, and the sources of the addresses by scheme

**Status**: accepted, implemented for the git import (§1–§3); proposed for the sources by scheme (§4–§6) ·
**Date**: 2026-10 · **Extends**: ADR 0147 (git repositories in the graph), ADR 0150 (declarations), ADR 0154
(logical URLs)

## Context

The git import (ADR 0147) made a node of every file, but kept of its content only the object id of its blob, its
size and its mode: a README, a YAML file or a script cannot be read in the graph. What a file is was told by its name
only (its extension), which says nothing of a `LICENSE`, a `.dat` or a `.txt` written in UTF-16.

The logical URLs of ADR 0154 (`lnk:<source>/<type>/<key>`) are read by the resolver of their source's kind, which
tells what the target is (`lnk.Target`). Other addresses (`file://…`, `https://…`) have no source: nobody tells the
user interface whether it can open them, and how. And no target says how it opens: the interface has to guess from
its kind.

The import also read the files one at a time from one `git cat-file` process, parsed at most 8 at once, and read the
files of a commit only once the change of the commit before was written.

## Decision

### 1. A file is text or a blob by its bytes

`contracts/content` (a leaf package) tells what bytes are: `Sniff(name, data)` on their first 8 KiB (`SniffBytes`):

- a byte order mark gives the encoding (UTF-8, UTF-16 LE/BE, UTF-32 LE/BE);
- else a NUL byte, or the signature of a binary format (`http.DetectContentType`: an image, an archive, a PDF, a
  font...) makes a blob;
- else valid UTF-8 with at most one control character per thousand bytes (tabs, line ends, form feeds and escapes
  are text) is text, and so is Windows-1252 with none.

`Decode` gives the text in UTF-8; `MediaOf` / `LanguageOf` read the media type and the language of a text from its
name, through fixed tables (the same on every machine, never the system's MIME files).

### 2. A text file keeps its text, a blob the address of its content

`code` 1.6.0 adds to `code@File`: `media`, `binary`, `encoding`, `content` (type `code`: the text decoded to UTF-8 in
its language, ADR 0152) and `contentUrl` (widget `url`). The importer (`Importer.Text`, `MaxTextBytes`, default
256 KiB; params `text`, `maxText`) keeps the text of a file that reads as text and is no larger; a larger text and
anything else is a blob named by its object id. A symbolic link keeps no text. `contentUrl` is where the content of
the version is opened (`Importer.ContentURL`, `ContentURLOf`):

- a template (param `contentUrl`, flag `-content-url`) with `{commit}`, `{path}`, `{blob}`, `{name}`;
- else, for a repository of github.com, gitlab.com, bitbucket.org or codeberg.org, the raw file at the commit;
- else, for a working tree read in place, the `file://` URL of the file in it (what is checked out there now).

A file whose blob did not change is not written again, so a repository imported before keeps its files without
content until they change, or until it is imported again.

### 3. The reads run in parallel and ahead of the writes

What the import reads of a file depends on the commit only, never on the graph. `prepare` reads the files a commit
adds or modifies with `Importer.Parallel` workers (default: the processors, no cap), each through its own
`git cat-file --batch` process (`readers`, `Blobs.ReadUpTo`: the size and, up to the larger of the text and symbol
limits, the content; else the first 8 KiB, the rest dropped): it judges the bytes, decodes the text and parses a
source file with Tree-sitter. `pipeline` reads the files of the next commit while the change of the current one is
written (one commit ahead).

The queries of ADR 0150 also read what they left out: the fields of C, C++ and Rust structs (and Go's embedded
fields), the attributes of a Python class, the fields of a JavaScript or TypeScript class and the properties of a
TypeScript interface (kind `field`); the variables of a C++ namespace and `export var`; the enumerators of C and C++,
the constants of a Java enum and of an interface (kind `constant`). `variable` stays a declaration of the top level.

### 4. A source serves schemes (proposed)

A source declares the schemes of the addresses it serves (`lnk.Source.Schemes`, attribute `schemes` of
`platform@Source`). An address that is not `lnk:` goes to the source declaring its scheme, else to the built-in one:

| Source | Kind | Schemes | Its resolver |
| --- | --- | --- | --- |
| `local-fs` | `local-fs` | `file` | reads a file only under the roots allowed (`GOAP_LOCAL_FS_ROOTS`, `params.roots`); elsewhere gives the path, unread |
| `web` | `web` | `http`, `https` | fetches nothing on the server: gives the address and the media type its path says |

A resolver serving schemes implements `lnk.AddressResolver` (`ResolveAddress(ctx, src, *url.URL)`).

### 5. A target says how it opens (proposed)

`lnk.Target.Open` (`Mode`, `Href`, `Media`) and `Target.Content`: `node` (open its editor), `text` (the text read,
under 1 MiB), `inline` (an image read, as a `data:` address, under 4 MiB), `external` (a web address, a new tab),
`path` (show the path: outside the roots, too large, or a blob). `Graph.ResolveAddress(project, address)` and the RPC
`GraphService.ResolveAddress` resolve a `lnk:` URL through the project's mapping (ADR 0154 §8) or any other address
through its scheme.

### 6. The interface applies the mode (proposed)

An attribute with `widget: url` (`def.WidgetURL`) is shown as a link; a click resolves it and does what its mode
says (`web/src/lib/address.ts`, `AddressLink.svelte`).

## Consequences

- The text of a repository is readable, searchable and diffable in the graph; the database grows with each version
  of a text file (capped per file; `text: false` turns it off).
- The server never reads a disk nor the web it was not allowed to: no root, no read; the web source fetches nothing.
- A project may later map a scheme to another source (a `file://` read on a network share), like the source names of
  ADR 0154 §8.
