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
   `code`, default on; not in the fingerprint, the digest is of the whole text).
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
   already, the packages and the deepest nodes first), then the other links, since a declaration may call one written
   later in the change.

## Consequences

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
