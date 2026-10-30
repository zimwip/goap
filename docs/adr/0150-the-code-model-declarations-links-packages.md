# ADR 0150 — The code model: declarations by type, their links, the packages

**Status**: accepted; implemented · **Date**: 2026-10

## Context

The git importer (ADR 0147) read the declarations of the source files as `code@Symbol` nodes a file `defines`, one
kind attribute telling a function from a struct, the imports among them. Nothing said what a declaration calls or
uses, which package a file belongs to or what it imports, and a node carried no text: the graph knew a function by its
first line and a digest. To navigate a code base (who calls this, what does this package depend on, what changed in
this method) the model needs the declarations as typed nodes, the links between them and the packages, versioned
commit after commit like the files.

## Decision

1. **A node type per kind of declaration** (`code` 1.3.0): `Function` (kinds function, prototype), `Method`, `Class`,
   `Struct` (struct, union), `Interface`, `Enum`, `Type`, `Variable`, `Constant`, `Field`, each extending `Symbol`;
   a namespace, a module and a macro stay a `Symbol`. The kind is still an attribute and still in the key
   (`<file>#<kind>:<qualified name>`), so a key always has the same type. Imports are no longer declarations: they
   become links to packages. A declaration keeps its source text in `code` (cut at 64 KiB; `Importer.Code`, param
   `code`, default on; not in the fingerprint, the digest is of the whole text). The text of a declaration holding
   others (a class, a struct, a namespace) is its own: the spans of the declarations it holds are left out of its
   signature, digest and code, with the lines they leave blank (`ownText`, `ownTexts` in `treesitter.go`). A method
   edited, added or removed gives a new version to that method alone, never to its class, whose members say they
   are its own through `member_of`; the class changes with its own text (a base, a decorator, a class attribute).
2. **Links between declarations**, held by the declaration (a version carries its links, ADR 0003): `member_of` its
   class, struct, interface or namespace (a Go method its receiver type, a C++ member defined out of its class `A::f`
   its class `A`), `calls` a function, a method, a macro or a class it makes, `uses` a type, a variable or a constant
   it names. Tree-sitter captures the names (`@ref.call`, `@ref.type`, `@ref.name`, with what they are selected from
   as `@qual`, one query per language with the declarations) and each name goes to the innermost declaration holding
   it; the importer resolves it by name, no type checking: through the import its qualifier names, `self` / `this` /
   a Go receiver to the members of the declaration's class, a static member of a type (`T::new`), the members of the
   class without `this` (Java, C++), the file, the names the file imports (`from x import f`, `import {f}`, `use a::f`),
   the files a C file includes, the package (Go, Java, C, C++). A name resolving to more than four declarations is
   left unresolved, an unknown receiver (`x.f()` with `x` a variable) too: a link is a claim, better none than a wrong
   one.
3. **Packages** (`code@Package`, key `<repo>@<family>:<path>`, attributes `name`, `path`, `language`, lifecycle
   `presence`): the files of a directory (Go: its import path, the module path of the nearest `go.mod` plus the
   directory; Python: the dotted path of the directory; JavaScript, TypeScript, C, C++, Rust: the directory), the
   declared package of a Java file, or what the repository imports from outside (a Go import path, a Python top-level
   module, an npm package, a system header, a crate). C and C++ share their packages, so do JavaScript and TypeScript.
   A file is `in_package` its package and `imports` the packages its imports resolve to (`platform/gitimport/packages.go`:
   relative imports, `__init__.py`, `index.ts`, include directories, `crate::` / `super::`); a package `depends_on` the
   packages its files import (itself left out), present while a file is in it or imports it, `removed` otherwise.
4. **Only what a commit changes changes.** The declarations of the files a commit adds or changes are read again,
   with their links; a declaration whose text and links are unchanged keeps its version, a declaration that
   disappears moves to `removed` and keeps its node (the same key, present again, when it comes back). A declaration
   of another file naming a declaration the commit changes or removes is not touched: its link names the version it
   named, suspect from then on (ADR 0003, drawn dashed by the web), until its own text changes and its links are
   computed again (then pointing at the head's versions). So does a file's `in_package` when its package's
   dependencies change. Re-versioning the callers would cascade through the call graph, and a file cannot take a new
   version its content does not have (a draft equal to its base is no modification, ADR 0102). A run with nothing new
   writes nothing: no change, no node.
5. **Parsing in parallel.** `TreeSitter.Parse` may be called from several goroutines (a free list of parser sets, one
   per goroutine at a time); the importer reads the contents of a commit's source files from one `git cat-file`, in
   order, and parses them on `Importer.Parallel` workers (default: the processors, at most 8; `-parallel`).
6. **Writing a commit in two passes**: every node first (creations carry the links whose targets have a draft
   already: the packages, then the declarations, then the files and directories, deepest first, the root last), then
   the other links, since a declaration may call one written later in the change. A node written renews the links it
   holds to an older version of their target than the one it will hold.
7. **A large commit is written by sub-changes** (ADR 0103). An operation on a change costs in proportion to the
   impacts it holds, so the first commit of a repository (this one: 31 851 nodes) written as one change is quadratic.
   A commit of more than `Importer.Batch` operations (default 1000, `-batch`) keeps its change, the change of the
   commit, and writes it through sub-changes merged into it in turn: one per file, a declaration with its file, the
   files naming each other's declarations together (a strongly connected component: in Go, the files of a package),
   each after the files it links to, the packages first, the directories and the root last; small files that follow
   each other share a sub-change, up to half a batch. The change of the commit holds every impact at the end and is
   merged into main once: still one change per commit, and a cancel abandons it with its sub-change at work, nothing
   reaching main. The importer commits and merges by header (`CommitChangeHeader`, `IntegrateChange`), never reading
   a whole state. The graph gained what this asks of it: a version of a merged branch no longer holds its key (the
   versions live on in the branches it joined, so a sub-change merged into an abandoned change frees its keys), the
   merge of a sub-change finds the parent's impacts by node, and an operation on the main flow no longer copies the
   impacts it sees nor looks a draft up among all the drafts of the change.

## Consequences

- Measured on this repository (SQLite, symbols and code): the first commit in 2 min 40, twenty commits in 3 min 20,
  imported again with nothing new: no change.
- `goap-gitimport -symbols` or the *Symbols* param of the IDE import gives the declarations, the calls and uses
  between them, the packages and their dependencies; the tests import a Go module (members, calls across packages
  through the import, packages and their dependencies, a rename leaving a suspect call, a run with nothing new) and
  Python, TypeScript and C files resolved through their imports.
- A repository imported before `code` 1.3.0 keeps its symbols typed `Symbol` (a node's type does not change) and its
  import symbols until their files change; import it again in a new namespace for the new types.
- Not done: type information (a call on a variable stays unresolved), the declarations of a file read again when a
  declaration it names moves (the suspect link stays until its own text changes), a module path changed in `go.mod`
  (the packages of unchanged files keep their path), the packages shared across repositories (each repository has its
  own, external ones included).

## Addendum: Vue and Svelte components

A Vue single-file component (`.vue`) or a Svelte component (`.svelte`) is read through its scripts: the file is parsed
with the grammar of its markup (Vue's top level is HTML, `tree-sitter-html`; Svelte has its own grammar,
`tree-sitter-svelte`, which reads `{#if}` blocks and `{expr}` attributes), its `<script>` blocks found, then parsed
together with the JavaScript or TypeScript grammar over their ranges of the file (Tree-sitter's included ranges, the
mechanism of its language injection): the offsets, the text and the code of the declarations stay those of the
component file. The language of the scripts is their `lang` attribute (`ts` / `typescript`, `tsx`, else JavaScript),
given as `Source.Language` and used for the code text of the declarations; the symbols keep the file's language
(`vue`, `svelte`). The components are of the `js` family: their imports and packages resolve as JavaScript's. The
markup and the `<style>` blocks give no declaration (the code domain has no kind for a CSS rule, and the importer reads
no `.css` file either), so they are not parsed. An Angular component is a TypeScript file, read as one; its templates
(`.html`) are not read: they declare nothing, and `tree-sitter-angular` has no Go binding. `tree-sitter-vue` has none
either, hence the HTML grammar for Vue. `.mts` and `.cts` files are read as TypeScript.
