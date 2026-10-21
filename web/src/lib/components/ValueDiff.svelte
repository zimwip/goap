<script lang="ts" module>
  import { loadRaw, save } from '../shell/storage';

  const VIEW_KEY = 'goap.ide.diff.view';
  /** unified or side by side, one choice for every diff of the page, remembered */
  const layout = $state({ split: loadRaw(VIEW_KEY) === 'split' });
</script>

<script lang="ts">
  // What changed between two values of an attribute, shown the way the attribute asks (its `diff`, else the usual one
  // of its type, valueDiff.ts): a short value on one line (old → new, or the words changed marked), a longer text, JSON
  // or source code as a diff of its lines, unified or side by side, the unchanged lines far from a change folded, code
  // highlighted in the language its value names. An absent side (undefined or null) is a value added or removed.
  import { attributeOf, shownValue, type AttributeView } from '../attributes';
  import { loadTypes, typeCatalog } from '../stores/types.svelte';
  import { editorLanguage, languageLabel } from '../codeText';
  import { highlightLines, type Token } from '../codemirror';
  import {
    counts,
    defaultDiff,
    diffSides,
    foldRows,
    isFold,
    isInline,
    lineDiff,
    splitRows,
    wordDiff,
    type DiffMode,
    type LineRow,
    type Segment,
  } from '../valueDiff';

  let {
    before,
    after,
    attribute,
    type,
    name,
    mode,
    maxHeight = '24rem',
    context = 3,
  }: {
    /** the old value (undefined or null: none, the value is added) */
    before?: unknown;
    /** the new value (undefined or null: none, the value is removed) */
    after?: unknown;
    /** the attribute the values are of: its diff, its enum labels */
    attribute?: AttributeView;
    /** else the node type and the property whose attribute it is, read in the type catalogue */
    type?: string;
    name?: string;
    /** the diff to use, over the attribute's */
    mode?: DiffMode;
    maxHeight?: string;
    /** unchanged lines kept around a change */
    context?: number;
  } = $props();

  const split = $derived(layout.split);
  let full = $state(false);
  let more = $state(false);
  let opened = $state<ReadonlySet<number>>(new Set());

  $effect(() => {
    if (!attribute && type && !typeCatalog.loaded) void loadTypes().catch(() => {});
  });
  const attr = $derived(attribute ?? (type ? attributeOf(typeCatalog.cat.attributes(type), name) : undefined));

  const has = (v: unknown) => v !== undefined && v !== null;
  const how = $derived<DiffMode>(mode ?? attr?.diff ?? defaultDiff(undefined));
  const sides = $derived(diffSides(how, has(before) ? before : undefined, has(after) ? after : undefined));
  const inline = $derived(how === 'value' || ((how === 'words' || how === 'lines' || how === 'json') && isInline(sides)));
  /** prose diffed by its words in a block (a long text whose attribute diffs by words) */
  const prose = $derived(!inline && how === 'words');

  const rows = $derived(inline || prose ? [] : lineDiff(sides.before, sides.after, how !== 'code'));
  const shown = $derived(full ? rows : foldRows(rows, context, opened));
  const tally = $derived(counts(rows));
  const lang = $derived(editorLanguage(sides.language));
  const hlBefore = $derived(how === 'code' ? highlightLines(sides.before, lang) : []);
  const hlAfter = $derived(how === 'code' ? highlightLines(sides.after, lang) : []);

  const words = $derived(inline && how !== 'value' && has(before) && has(after) ? wordDiff(sides.before, sides.after) : prose ? wordDiff(sides.before, sides.after) : []);

  const CLIP = 120;
  const valueText = (v: unknown) => (!has(v) ? '' : how === 'value' ? shownValue(attr, v) || '""' : typeof v === 'string' ? v || '""' : JSON.stringify(v));
  const clipped = (t: string) => (!more && t.length > CLIP ? `${t.slice(0, CLIP)}…` : t);
  const long = $derived(valueText(before).length > CLIP || valueText(after).length > CLIP);

  function setView(s: boolean) {
    layout.split = s;
    save(VIEW_KEY, s ? 'split' : 'unified');
  }

  function open(from: number) {
    const next = new Set(opened);
    next.add(from);
    opened = next;
  }

  const tokensOf = (r: LineRow, side: 'old' | 'new'): Token[] | undefined =>
    how !== 'code' ? undefined : side === 'old' ? hlBefore[(r.old ?? 0) - 1] : hlAfter[(r.new ?? 0) - 1];
  const sign = (r: LineRow) => (r.op === 'add' ? '+' : r.op === 'del' ? '−' : ' ');
</script>

{#snippet segments(list: Segment[])}{#each list as s, i (i)}{#if s.op === 'add'}<ins>{s.text}</ins>{:else if s.op === 'del'}<del>{s.text}</del>{:else}{s.text}{/if}{/each}{/snippet}

{#snippet line(r: LineRow, side: 'old' | 'new')}
  {@const toks = tokensOf(r, side)}
  {#if toks}{#each toks as t, i (i)}<span class={t.cls}>{t.text}</span>{/each}{:else if r.words}{@render segments(r.words)}{:else}{r.text}{/if}
{/snippet}

{#snippet fold(from: number, to: number, cols: number)}
  <tr class="fold">
    <td colspan={cols}><button type="button" class="link small" onclick={() => open(from)}>⋯ {to - from} unchanged line{to - from === 1 ? '' : 's'}</button></td>
  </tr>
{/snippet}

{#if inline}
  <span class="vd-inline">
    {#if words.length}
      <code class="words">{@render segments(words)}</code>
    {:else}
      {#if has(before)}<code class="old">{clipped(valueText(before))}</code>{/if}
      {#if has(before) && has(after)}<span class="arrow">→</span>{/if}
      {#if has(after)}<code class="new">{clipped(valueText(after))}</code>{/if}
      {#if long}<button type="button" class="link small" onclick={() => (more = !more)}>{more ? 'less' : 'more'}</button>{/if}
    {/if}
  </span>
{:else}
  <div class="vd" style:--vd-max={maxHeight}>
    <div class="head">
      {#if how === 'code'}
        {#if sides.language}<span class="lang mono" title="Language">{languageLabel(sides.language)}</span>{:else}<span class="hint">no language</span>{/if}
      {/if}
      {#if prose}
        <span class="hint">{!has(before) ? 'added' : !has(after) ? 'removed' : 'words changed'}</span>
      {:else}
        <span class="tally"><span class="plus">+{tally.added}</span> <span class="minus">−{tally.removed}</span></span>
      {/if}
      {#each sides.meta as m (m)}<span class="hint mono" title="Front matter">{m}</span>{/each}
      <span class="grow"></span>
      {#if !prose}
        {#if rows.length > shown.length || full}
          <button type="button" class="link small" onclick={() => (full = !full)}>{full ? 'changes only' : 'all lines'}</button>
        {/if}
        <span class="views" role="group" aria-label="Layout of the diff">
          <button type="button" class="small ghost" class:on={!split} aria-pressed={!split} onclick={() => setView(false)}>Unified</button>
          <button type="button" class="small ghost" class:on={split} aria-pressed={split} onclick={() => setView(true)}>Split</button>
        </span>
      {/if}
    </div>
    <div class="body">
      {#if prose}
        <pre class="prose">{@render segments(words)}</pre>
      {:else if !rows.length}
        <p class="hint">Both are empty.</p>
      {:else if split}
        <table class="lines split">
          <tbody>
            {#each splitRows(shown) as r, i (i)}
              {#if isFold(r)}
                {@render fold(r.from, r.to, 4)}
              {:else}
                <tr>
                  <td class="no">{r.left?.old ?? ''}</td>
                  <td class="txt {r.left ? (r.left.op === 'same' ? '' : 'del') : 'none'}">{#if r.left}{@render line(r.left, 'old')}{/if}</td>
                  <td class="no">{r.right?.new ?? ''}</td>
                  <td class="txt {r.right ? (r.right.op === 'same' ? '' : 'add') : 'none'}">{#if r.right}{@render line(r.right, 'new')}{/if}</td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      {:else}
        <table class="lines">
          <tbody>
            {#each shown as r, i (i)}
              {#if isFold(r)}
                {@render fold(r.from, r.to, 4)}
              {:else}
                <tr class={r.op}>
                  <td class="no">{r.old ?? ''}</td>
                  <td class="no">{r.new ?? ''}</td>
                  <td class="sign" aria-hidden="true">{sign(r)}</td>
                  <td class="txt">{@render line(r, r.op === 'del' ? 'old' : 'new')}</td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  </div>
{/if}

<style>
  .vd-inline {
    display: inline-flex;
    gap: 6px;
    align-items: baseline;
    flex-wrap: wrap;
    min-width: 0;
  }
  .vd-inline code {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .vd-inline .old {
    color: var(--danger);
    text-decoration: line-through;
  }
  .vd-inline .new {
    color: var(--ok);
  }
  .arrow {
    color: var(--muted);
  }
  ins {
    text-decoration: none;
    background: var(--ok-soft);
    color: var(--ok);
    border-radius: 2px;
  }
  del {
    background: var(--danger-soft);
    color: var(--danger);
    border-radius: 2px;
  }
  .vd {
    display: grid;
    gap: 0.25rem;
    min-width: 0;
    width: 100%;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    font-size: 0.8rem;
  }
  .lang {
    padding: 0 0.4rem;
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    color: var(--text);
  }
  .tally {
    font-family: var(--mono);
  }
  .plus {
    color: var(--ok);
  }
  .minus {
    color: var(--danger);
  }
  .grow {
    flex: 1;
  }
  .views {
    display: inline-flex;
    gap: 2px;
  }
  .views .on {
    background: var(--accent-soft);
    color: var(--text);
  }
  .body {
    max-height: var(--vd-max);
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
  }
  .prose {
    margin: 0;
    padding: 0.4rem 0.6rem;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-family: var(--font);
  }
  .lines {
    width: 100%;
    border-collapse: collapse;
    font-family: var(--mono);
    font-size: 12.5px;
    line-height: 1.5;
    tab-size: 4;
  }
  .lines td {
    padding: 0 0.4rem;
    border: none;
    vertical-align: top;
  }
  .no {
    width: 1%;
    min-width: 2.5em;
    text-align: right;
    color: var(--muted);
    background: var(--surface-2);
    user-select: none;
    white-space: nowrap;
  }
  .sign {
    width: 1%;
    user-select: none;
    color: var(--muted);
  }
  .txt {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .split .txt {
    width: 50%;
  }
  tr.add .txt,
  tr.add .sign,
  .txt.add {
    background: var(--ok-soft);
  }
  tr.del .txt,
  tr.del .sign,
  .txt.del {
    background: var(--danger-soft);
  }
  .txt.none {
    background: var(--surface-2);
  }
  /* within a line: the words changed, stronger than the line's own colour */
  .txt :global(ins),
  .txt :global(del) {
    font-weight: 600;
  }
  .fold td {
    background: var(--surface-2);
    text-align: center;
  }
  .syn-keyword {
    color: var(--syn-keyword);
    font-weight: 600;
  }
  .syn-string {
    color: var(--syn-string);
  }
  .syn-number {
    color: var(--syn-number);
  }
  .syn-comment {
    color: var(--syn-comment);
    font-style: italic;
  }
  .syn-function {
    color: var(--syn-function);
  }
  .syn-type {
    color: var(--syn-type);
  }
  .syn-property {
    color: var(--syn-property);
  }
  .syn-def {
    color: var(--syn-def);
  }
  .syn-punct {
    color: var(--muted);
  }
</style>
