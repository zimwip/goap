# ADR 0152 — Code attributes

**Status**: accepted; implemented · **Date**: 2026-10

## Context

The git importer (ADR 0147, ADR 0150) keeps the source text of each declaration on its `code@Symbol` node, in a string
attribute edited as a text area. A string says nothing of the language of the text, so nothing can highlight it, and
the text of a member (a method of a class) kept the indentation it had in its file past its first line, which the
importer cut at the start of the declaration: the first line flush left, the others indented.

## Decision

1. **An attribute type `code`** (`def.AttrCode`, widget `def.WidgetCode`, its usual widget): the value stays a string, a
   property like any other, written as a front matter giving at least the language, then the text as a fenced Markdown
   code block:

   ````
   ---
   language: go
   ---
   ```go
   func F() {}
   ```
   ````

   The fence is longer than any run of backticks of the text; the front matter is `key: value` lines (the language
   first, the other keys kept and sorted; a value quoted when it must be). `contracts/codetext` writes and reads it
   (`Format`, `Parse`, `Block` for the fenced block alone); `web/src/lib/codeText.ts` is its twin. A plain string with no
   front matter reads as code of no known language, so a string attribute can become a code attribute without
   rewriting its values; a block alone takes its info string as the language.
2. **The graph checks it** (`domain.AttributeCheck`, type `code`): a string that reads, refusing a front matter not
   closed or not made of `key: value` lines, and text after the block.
3. **The git importer writes code texts** (`code` 1.5.0: `Symbol.code` of type `code`): the language of its file, and
   the text of the declaration dedented to its own indentation (`leadOf` gives the indentation of the line it starts
   at, `dedent` removes what all its lines share), so a method reads as if declared at the top level. The digest is
   still of the text as in its file. A repository imported before gets a new version of each symbol at the next commit
   that changes its file.
4. **The web shows and edits code** (`attributes.isCode`): the node tab shows the value with its language, line
   numbers, highlighting and its indentation (tabs four columns wide), and a copy button (`CodeView.svelte`); the
   property form edits the language (a known one or any name) and the text in CodeMirror, Tab indenting
   (`CodeValueEditor.svelte`), and leaves a value it did not edit as it came. CodeMirror highlights the languages of
   the importer: C, C++, Go, Java, JavaScript, Python, Rust, TypeScript, TSX (`codemirror.ts`, `editorLanguage`);
   another language is shown as plain text.
5. **The OKF export** (ADR 0111) renders a code attribute as a section of the body holding its fenced block, the front
   matter left out (`okf.BodyAttribute.Code`).

## Consequences

- A domain declares source text with `type: code`; the attribute and type editors offer it.
- Not done: a form widget `code` for the forms of human actions (ADR 0115), languages beyond those above, line
  numbers of the declaration in its file (a symbol keeps none, ADR 0150).
