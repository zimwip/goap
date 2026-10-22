// CodeMirror 6 configuration: theme aligned with the application's CSS tokens
// (light / dark), JavaScript syntax highlighting and `ctx` DSL completion, and the
// languages of the source text of code attributes (codeText.ts).
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, placeholder as placeholderExt } from '@codemirror/view';
import { EditorState, Compartment, Prec, type Extension } from '@codemirror/state';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import {
  HighlightStyle,
  syntaxHighlighting,
  indentOnInput,
  bracketMatching,
  foldGutter,
  foldKeymap,
  LanguageSupport,
  type Language,
} from '@codemirror/language';
import {
  autocompletion,
  closeBrackets,
  closeBracketsKeymap,
  completionKeymap,
  snippetCompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
} from '@codemirror/autocomplete';
import { javascript, javascriptLanguage } from '@codemirror/lang-javascript';
import { cpp } from '@codemirror/lang-cpp';
import { go } from '@codemirror/lang-go';
import { java } from '@codemirror/lang-java';
import { python } from '@codemirror/lang-python';
import { rust } from '@codemirror/lang-rust';
import { highlightCode, tagHighlighter, tags as t } from '@lezer/highlight';
import { DSL_FIELDS, DSL_FUNCTIONS, algorithmUsage, type DslFunction } from './dsl';

/** The languages of code attributes highlighted (the names the git importer gives them, CODE_LANGUAGES). */
export type SourceLanguage = 'c' | 'cpp' | 'go' | 'java' | 'python' | 'rust' | 'typescript' | 'tsx';
export type CodeLanguage = 'javascript' | 'cel' | 'text' | SourceLanguage;

// --- theme ----------------------------------------------------------------------

const theme = EditorView.theme({
  '&': {
    color: 'var(--text)',
    backgroundColor: 'var(--surface)',
    fontSize: '12.5px',
    border: '1px solid var(--border)',
    borderRadius: 'var(--radius-sm)',
  },
  '&.cm-focused': { outline: '2px solid var(--accent)', outlineOffset: '1px' },
  '.cm-scroller': { fontFamily: 'var(--mono)', lineHeight: '1.55' },
  '.cm-content': { caretColor: 'var(--text)', padding: '4px 0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--text)' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
    backgroundColor: 'var(--selection) !important',
  },
  '.cm-gutters': {
    backgroundColor: 'var(--surface-2)',
    color: 'var(--muted)',
    border: 'none',
    borderRight: '1px solid var(--border)',
  },
  '.cm-activeLine': { backgroundColor: 'var(--active-line)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--active-line)', color: 'var(--text)' },
  '.cm-matchingBracket': { backgroundColor: 'var(--accent-soft)', outline: '1px solid var(--accent)' },
  '.cm-placeholder': { color: 'var(--muted)' },
  '.cm-tooltip': {
    backgroundColor: 'var(--surface)',
    border: '1px solid var(--border)',
    color: 'var(--text)',
    boxShadow: 'var(--shadow-pop)',
  },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--accent)', color: 'var(--accent-text)' },
  '.cm-completionDetail': { color: 'var(--muted)', fontStyle: 'normal', marginLeft: '0.6em' },
  '.cm-tooltip.cm-completionInfo': { maxWidth: '320px', padding: '4px 8px', fontFamily: 'var(--font)' },
  '&.cm-editor[aria-readonly=true] .cm-content, &.readonly .cm-content': { opacity: '0.85' },
});

// the tags highlighted, by the token class outside an editor (`syn-<class>`, styled by the diff of code values)
const SYNTAX = [
  { tag: [t.keyword, t.controlKeyword, t.moduleKeyword, t.definitionKeyword, t.operatorKeyword], cls: 'keyword', color: 'var(--syn-keyword)', fontWeight: '600' },
  { tag: [t.string, t.special(t.string), t.regexp], cls: 'string', color: 'var(--syn-string)' },
  { tag: [t.number, t.bool, t.null, t.atom], cls: 'number', color: 'var(--syn-number)' },
  { tag: [t.lineComment, t.blockComment, t.comment], cls: 'comment', color: 'var(--syn-comment)', fontStyle: 'italic' },
  { tag: [t.function(t.variableName), t.function(t.propertyName)], cls: 'function', color: 'var(--syn-function)' },
  { tag: [t.typeName, t.className, t.namespace], cls: 'type', color: 'var(--syn-type)' },
  { tag: [t.propertyName], cls: 'property', color: 'var(--syn-property)' },
  { tag: [t.definition(t.variableName)], cls: 'def', color: 'var(--syn-def)' },
  { tag: [t.operator, t.punctuation, t.bracket], cls: 'punct', color: 'var(--muted)' },
];

const highlight = HighlightStyle.define(SYNTAX.map(({ cls: _cls, ...spec }) => spec));
const tokenClasses = tagHighlighter(SYNTAX.map(({ tag, cls }) => ({ tag, class: `syn-${cls}` })));

// --- DSL completion -------------------------------------------------------------

function dslOptions(functions: DslFunction[]): Completion[] {
  return functions.map((f) => {
    const name = f.name;
    const args = f.args.map((a, i) => `\${${i + 1}:${a.replace(/[{}]/g, '')}}`).join(', ');
    return snippetCompletion(`${name}(${args})`, {
      label: name,
      type: 'function',
      detail: `(${f.args.join(', ')})${f.returns ? ` → ${f.returns}` : ''}`,
      info: f.doc,
      section: f.group,
    });
  });
}

const FIELD_OPTIONS: Completion[] = [...new Set(Object.values(DSL_FIELDS).flat())].map((name) => ({
  label: name,
  type: 'property',
  detail: Object.entries(DSL_FIELDS)
    .filter(([, fields]) => fields.includes(name))
    .map(([k]) => k)
    .join(' · '),
}));

function dslSource(functions: DslFunction[]) {
  const fns = dslOptions(functions);
  const fields = FIELD_OPTIONS;
  return (ctx: CompletionContext): CompletionResult | null => {
    const m = ctx.matchBefore(/\bctx\.\w*$/);
    if (m) return { from: m.from + 4, options: fns, validFor: /^\w*$/ };
    // Fields of Item / Node / Link objects after "x." (other than ctx).
    const f = ctx.matchBefore(/\b[a-zA-Z_]\w*\.\w*$/);
    if (f && ctx.explicit) return { from: f.from + f.text.indexOf('.') + 1, options: fields, validFor: /^\w*$/ };
    if (f && f.text.length > f.text.indexOf('.') + 1)
      return { from: f.from + f.text.indexOf('.') + 1, options: fields, validFor: /^\w*$/ };
    return null;
  };
}

// --- extensions ------------------------------------------------------------------------

/** dsl: true = the ctx of script actions; an algorithm usage = the ctx of that usage (ADR 0018). */
export function languageExtension(lang: CodeLanguage, dsl: boolean | string): Extension {
  const functions = typeof dsl === 'string' ? (algorithmUsage(dsl)?.functions ?? []) : DSL_FUNCTIONS;
  switch (lang) {
    case 'javascript':
      return [javascript(), dsl ? javascriptLanguage.data.of({ autocomplete: dslSource(functions) }) : []];
    case 'cel':
      // CEL has an expression syntax close to JavaScript: highlighting is good enough.
      return javascript();
    case 'c':
    case 'cpp':
      return cpp();
    case 'go':
      return go();
    case 'java':
      return java();
    case 'python':
      return python();
    case 'rust':
      return rust();
    case 'typescript':
      return javascript({ typescript: true });
    case 'tsx':
      return javascript({ typescript: true, jsx: true });
    default:
      return [];
  }
}

export function baseExtensions(opts: { lineNumbers: boolean; placeholder?: string; wrap?: boolean }): Extension[] {
  return [
    opts.lineNumbers ? [lineNumbers(), foldGutter(), highlightActiveLineGutter()] : [],
    history(),
    drawSelection(),
    indentOnInput(),
    bracketMatching(),
    closeBrackets(),
    autocompletion({ activateOnTyping: true }),
    highlightActiveLine(),
    syntaxHighlighting(highlight),
    theme,
    opts.wrap ? EditorView.lineWrapping : [],
    opts.placeholder ? placeholderExt(opts.placeholder) : [],
    EditorState.tabSize.of(opts.lineNumbers ? 4 : 2),
    keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, ...foldKeymap, ...completionKeymap, indentWithTab]),
  ];
}

/** What a completion function answers: where the word starts and what may stand there (cypherComplete.ts). */
export interface TextCompletions {
  from: number;
  options: { label: string; kind: string; detail?: string; info?: string; boost?: number }[];
}

const COMPLETION_TYPES: Record<string, string> = { type: 'class', link: 'interface', property: 'property', keyword: 'keyword', variable: 'variable', function: 'function' };

/** Completion from a function of the text and the cursor, whatever the language (`explicit`: asked with Ctrl+Space). */
export function completionFrom(fn: (text: string, pos: number, explicit: boolean) => TextCompletions | null): Extension {
  const source = (c: CompletionContext): CompletionResult | null => {
    const r = fn(c.state.doc.toString(), c.pos, c.explicit);
    if (!r || !r.options.length) return null;
    return {
      from: r.from,
      options: r.options.map((o) => ({ label: o.label, type: COMPLETION_TYPES[o.kind] ?? 'text', detail: o.detail, info: o.info, boost: o.boost })),
      validFor: /^`?[\w@]*`?$/,
    };
  };
  return EditorState.languageData.of(() => [{ autocomplete: source }]);
}

/** Runs `run` on Mod-Enter (the query boxes), before any other binding of the key. */
export function runKey(run: () => void): Extension {
  return Prec.highest(keymap.of([{ key: 'Mod-Enter', run: () => (run(), true) }]));
}

// --- highlighting outside an editor ------------------------------------------------

/** The language parsed for highlighting (none for plain text). */
export function languageOf(lang: CodeLanguage): Language | undefined {
  const ext = lang === 'javascript' || lang === 'cel' ? javascript() : languageExtension(lang, false);
  return ext instanceof LanguageSupport ? ext.language : undefined;
}

/** A piece of a highlighted line: its text and its token class (`syn-keyword`, `syn-string`… '' : none). */
export interface Token {
  text: string;
  cls: string;
}

/** The lines of a text highlighted in its language with the tags of the editor's style (the diff of code values), one
 * array of tokens per line. */
export function highlightLines(text: string, lang: CodeLanguage): Token[][] {
  const out: Token[][] = [[]];
  const language = languageOf(lang);
  if (!language) return text.split('\n').map((l) => (l ? [{ text: l, cls: '' }] : []));
  highlightCode(
    text,
    language.parser.parse(text),
    tokenClasses,
    (piece, cls) => out.at(-1)!.push({ text: piece, cls }),
    () => out.push([]),
  );
  return out;
}

export { EditorView, EditorState, Compartment };
