// CodeMirror 6 configuration: theme aligned with the application's CSS tokens
// (light / dark), JavaScript / Go syntax highlighting and `ctx` DSL completion.
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, placeholder as placeholderExt } from '@codemirror/view';
import { EditorState, Compartment, type Extension } from '@codemirror/state';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import {
  HighlightStyle,
  syntaxHighlighting,
  indentOnInput,
  bracketMatching,
  foldGutter,
  foldKeymap,
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
import { go, goLanguage } from '@codemirror/lang-go';
import { tags as t } from '@lezer/highlight';
import { DSL_FIELDS, DSL_FUNCTIONS, goName } from './dsl';

export type CodeLanguage = 'javascript' | 'go' | 'cel' | 'text';

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

const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.controlKeyword, t.moduleKeyword, t.definitionKeyword, t.operatorKeyword], color: 'var(--syn-keyword)', fontWeight: '600' },
  { tag: [t.string, t.special(t.string), t.regexp], color: 'var(--syn-string)' },
  { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--syn-number)' },
  { tag: [t.lineComment, t.blockComment, t.comment], color: 'var(--syn-comment)', fontStyle: 'italic' },
  { tag: [t.function(t.variableName), t.function(t.propertyName)], color: 'var(--syn-function)' },
  { tag: [t.typeName, t.className, t.namespace], color: 'var(--syn-type)' },
  { tag: [t.propertyName], color: 'var(--syn-property)' },
  { tag: [t.definition(t.variableName)], color: 'var(--syn-def)' },
  { tag: [t.operator, t.punctuation, t.bracket], color: 'var(--muted)' },
]);

// --- DSL completion -------------------------------------------------------------

function dslOptions(lang: 'javascript' | 'go'): Completion[] {
  return DSL_FUNCTIONS.map((f) => {
    const name = lang === 'go' ? goName(f.name) : f.name;
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

function dslSource(lang: 'javascript' | 'go') {
  const fns = dslOptions(lang);
  const fields = lang === 'go' ? FIELD_OPTIONS.map((o) => ({ ...o, label: goName(o.label) })) : FIELD_OPTIONS;
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

export function languageExtension(lang: CodeLanguage, dsl: boolean): Extension {
  switch (lang) {
    case 'javascript':
      return [javascript(), dsl ? javascriptLanguage.data.of({ autocomplete: dslSource('javascript') }) : []];
    case 'go':
      return [go(), dsl ? goLanguage.data.of({ autocomplete: dslSource('go') }) : []];
    case 'cel':
      // CEL has an expression syntax close to JavaScript: highlighting is good enough.
      return javascript();
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

export { EditorView, EditorState, Compartment };
