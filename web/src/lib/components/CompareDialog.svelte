<script lang="ts">
  import { untrack } from 'svelte';
  import { stamp, keyOf } from '../flux/signals.svelte';
  // Comparing and deciding the options of a change (ADR 0009 §3, ADR 0032 §6, ADR 0083): a dialog opened from the
  // scope bar. It compares the impacts two scopes (the main flow or an option) see, as added / removed / modified (the
  // identical ones are only counted), and decides the option on the right: evaluate, select or reject it. Opening an
  // option, looking at it and working on it (the active option) is the scope bar's.
  import { graph, errorMessage, shortId, type Flow, type FlowDiff, type ImpactDiff } from '../api';
  import { MAIN_SCOPE, scopeColor, scopeName } from '../changeScope';
  import {
    CATEGORY_LABEL,
    CATEGORIES,
    clip,
    countByCategory,
    defaultSides,
    describeChange,
    groupImpacts,
    identicalLine,
    reconcileSides,
    sameScope,
    scopeChoices,
    scopeOf,
    swapSides,
    toggleCategory,
    type Category,
    type Sides,
  } from '../flowDiff';
  import Icon from '../shell/Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';

  let {
    changeId,
    options,
    scope = MAIN_SCOPE,
    closed = false,
    onchange,
    onclose,
    onshow,
    onopennode,
  }: {
    changeId: string;
    options: Flow[];
    /** the scope the scope bar looks at: the right-hand side starts on it when it is an option */
    scope?: string;
    closed?: boolean;
    /** an option was evaluated, selected or rejected */
    onchange?: () => void;
    onclose?: () => void;
    /** shows an impact in the Impacts pane of a scope */
    onshow?: (scope: string, impact: ImpactDiff) => void;
    /** opens the node of an impact as a scope sees it */
    onopennode?: (scope: string, impact: ImpactDiff) => void;
  } = $props();

  let sides = $state<Sides>(untrack(() => defaultSides(options, scope)));
  let level = $state<'written' | 'accepted'>('written');
  let hidden = $state<ReadonlySet<Category>>(new Set());
  let diff = $state<FlowDiff | undefined>();
  let loading = $state(false);
  let error = $state('');
  let busy = $state('');
  let evaluating = $state('');
  let evaluation = $state('');
  let expanded = $state<ReadonlySet<string>>(new Set());
  let panel = $state<HTMLDivElement>();

  const choices = $derived(scopeChoices(options));
  const same = $derived(sameScope(sides));
  const rightOption = $derived(options.find((o) => o.id === sides.right));
  const decidable = $derived(!closed && rightOption?.status === 'open');
  const counts = $derived(countByCategory(diff?.impacts ?? []));
  const groups = $derived(groupImpacts(diff?.impacts ?? [], hidden));
  const nameOf = (id: string) => scopeName(options, id);

  // the options may change under the dialog (a decision, another tab)
  $effect(() => {
    const next = reconcileSides(untrack(() => sides), options, untrack(() => scope));
    if (next !== untrack(() => sides)) sides = next;
  });

  $effect(() => {
    queueMicrotask(() => panel?.focus());
  });

  $effect(() => {
    const { left, right } = sides;
    void level;
    void options;
    if (!changeId || left === right) {
      diff = undefined;
      return;
    }
    void stamp(keyOf.change(changeId));
    const ctrl = new AbortController();
    loading = true;
    graph
      .diffFlows(changeId, left, right, level, ctrl.signal)
      .then((d) => {
        diff = d;
        error = '';
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) loading = false;
      });
    return () => ctrl.abort();
  });

  async function act(label: string, fn: () => Promise<unknown>) {
    busy = label;
    error = '';
    try {
      await fn();
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  const evaluate = (id: string) =>
    act(`evaluate:${id}`, async () => {
      await graph.evaluateOption(changeId, id, evaluation.trim());
      evaluating = '';
      evaluation = '';
    });

  const select = (id: string) => act(`select:${id}`, () => graph.selectOption(changeId, id));
  const reject = (id: string) => act(`reject:${id}`, () => graph.rejectOption(changeId, id));

  function toggleExpanded(id: string) {
    const next = new Set(expanded);
    if (!next.delete(id)) next.add(id);
    expanded = next;
  }

  const sideLabel = (s: string) => (s === MAIN_SCOPE ? 'Main flow' : nameOf(s));
  const keyOfChange = (d: ImpactDiff, i: number) => `${d.node}:${i}`;
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose?.()} />

<div class="backdrop" role="presentation" onmousedown={() => onclose?.()}>
  <div class="panel" role="dialog" aria-modal="true" aria-label="Compare and decide" tabindex="-1" bind:this={panel} onmousedown={(e) => e.stopPropagation()}>
    <header class="head">
      <div class="title">
        <h2>Compare &amp; decide</h2>
        <span class="grow"></span>
        <label class="level" title="written: every impact not rejected, as the flow sees it; accepted: only the impacts accepted on that flow">
          Level
          <select bind:value={level}>
            <option value="written">written</option>
            <option value="accepted">accepted</option>
          </select>
        </label>
        <button type="button" class="ghost small" aria-label="Close" onclick={() => onclose?.()}><Icon name="x" size={14} /></button>
      </div>
      <div class="sides">
        <label class="side" style="--c: {scopeColor(options, sides.left)}">
          <span class="cap">Left</span>
          <select bind:value={sides.left} aria-label="Left scope">
            {#each choices as c (c.id)}<option value={c.id}>{c.label}{c.status ? ` (${c.status})` : ''}</option>{/each}
          </select>
        </label>
        <button type="button" class="swap" title="Swap the sides" aria-label="Swap the sides" onclick={() => (sides = swapSides(sides))}>⇄</button>
        <label class="side" style="--c: {scopeColor(options, sides.right)}">
          <span class="cap">Right</span>
          <select bind:value={sides.right} aria-label="Right scope">
            {#each choices as c (c.id)}<option value={c.id}>{c.label}{c.status ? ` (${c.status})` : ''}</option>{/each}
          </select>
        </label>
      </div>
      {#if rightOption}
        <div class="decide" style="--c: {scopeColor(options, sides.right)}">
          <span class="who"><strong>{rightOption.option?.name}</strong> <StatusBadge status={rightOption.optionStatus} /></span>
          {#if decidable}
            <button type="button" class="small" disabled={!!busy} onclick={() => ((evaluating = evaluating === rightOption.id ? '' : (rightOption.id ?? '')), (evaluation = rightOption.evaluation ?? ''))}>Evaluate</button>
            <button type="button" class="small primary" disabled={!!busy} title="Its drafts become the main flow's; the other open options are rejected" onclick={() => select(rightOption.id ?? '')}>Select</button>
            <button type="button" class="small danger" disabled={!!busy} title="Its flow is discarded" onclick={() => reject(rightOption.id ?? '')}>Reject</button>
          {:else if !closed}
            <span class="hint">decided: nothing left to decide on it</span>
          {/if}
          {#if rightOption.evaluation}<span class="hint eval" title={rightOption.evaluation}>Evaluation: {rightOption.evaluation}</span>{/if}
        </div>
        {#if evaluating === rightOption.id}
          <div class="decide">
            <input type="text" class="grow" placeholder="Criteria, scores, rationale" bind:value={evaluation} />
            <button type="button" class="small" disabled={!!busy || !evaluation.trim()} onclick={() => evaluate(rightOption.id ?? '')}>Record</button>
          </div>
        {/if}
      {/if}
    </header>

    <div class="body">
      {#if error}<div class="alert">{error}</div>{/if}

      {#if same}
        <p class="hint">The same scope on both sides: pick two different scopes to see how their impacts differ.</p>
      {:else}
        <div class="chips" role="group" aria-label="Show or hide a category">
          {#each CATEGORIES as c (c)}
            <button type="button" class="chip {c}" class:off={hidden.has(c)} aria-pressed={!hidden.has(c)} onclick={() => (hidden = toggleCategory(hidden, c))}>
              {CATEGORY_LABEL[c]} <span class="n">{counts[c]}</span>
            </button>
          {/each}
          {#if loading}<span class="hint">Comparing…</span>{/if}
        </div>

        {#if diff}
          {#if !diff.impacts?.length}
            <p class="hint">{sideLabel(sides.left)} and {sideLabel(sides.right)} see the same impacts{level === 'accepted' ? ' once accepted' : ''}.</p>
          {/if}
          {#each groups as g (g.category)}
            <section class="group {g.category}">
              <h4>{g.label} <span class="count">{g.total}</span>{#if hidden.has(g.category)} <span class="hint">hidden</span>{/if}</h4>
              {#if g.category === 'added'}<p class="hint small">only in {sideLabel(sides.right)}</p>{:else if g.category === 'removed'}<p class="hint small">only in {sideLabel(sides.left)}</p>{/if}
              <ul class="rows">
                {#each g.items as d (`${d.node ?? ''}:${d.key ?? ''}`)}
                  {@const at = scopeOf(d, sides)}
                  <li class="row {g.category}">
                    <div class="line">
                      <button type="button" class="link key" title="Show it in the Impacts pane of {nameOf(at)}" onclick={() => onshow?.(at, d)}>{d.key}</button>
                      <span class="muted type">{d.type}</span>
                      {#if d.left?.review || d.right?.review}
                        <span class="reviews" title="its review on the left and on the right">
                          {#if d.left}<StatusBadge status={d.left.review} />{/if}{#if d.left && d.right}<span class="muted">→</span>{/if}{#if d.right}<StatusBadge status={d.right.review} />{/if}
                        </span>
                      {/if}
                      <span class="grow"></span>
                      {#if d.category === 'modified'}
                        <button type="button" class="link small" onclick={() => onshow?.(sides.left, d)}>left</button>
                        <button type="button" class="link small" onclick={() => onshow?.(sides.right, d)}>right</button>
                      {/if}
                      <button type="button" class="link small" title="Open the node as {nameOf(at)} sees it" onclick={() => onopennode?.(at, d)}>open node</button>
                    </div>
                    {#if g.category === 'modified'}
                      <ul class="changes">
                        {#each d.changes ?? [] as c, i (keyOfChange(d, i))}
                          {@const l = describeChange(c)}
                          {@const id = keyOfChange(d, i)}
                          {@const o = clip(l.old ?? '')}
                          {@const n = clip(l.new ?? '')}
                          {@const open = expanded.has(id)}
                          <li class="change {l.op}">
                            <span class="op" aria-label={l.op}>{l.op === 'added' ? '+' : l.op === 'removed' ? '−' : '~'}</span>
                            <span class="name">{l.label}</span>
                            {#if l.old !== undefined || l.new !== undefined}
                              <span class="vals">
                                {#if l.old !== undefined}<code class="old">{open ? l.old : o.text}</code>{/if}
                                {#if l.old !== undefined && l.new !== undefined}<span class="arrow">→</span>{/if}
                                {#if l.new !== undefined}<code class="new">{open ? l.new : n.text}</code>{/if}
                                {#if o.clipped || n.clipped}<button type="button" class="link small" onclick={() => toggleExpanded(id)}>{open ? 'less' : 'more'}</button>{/if}
                              </span>
                            {/if}
                          </li>
                        {/each}
                      </ul>
                    {/if}
                  </li>
                {/each}
              </ul>
            </section>
          {/each}
          <p class="hint identical">{identicalLine(diff.identical ?? 0)}</p>
        {/if}
      {/if}

      <details class="more" open={!options.length}>
        <summary>Options <span class="count">{options.length}</span></summary>
        {#if options.length}
          <ul class="options">
            {#each options as o (o.id)}
              <li class:current={o.id === sides.right}>
                <div class="oline">
                  <StatusBadge status={o.optionStatus} />
                  <strong>{o.option?.name}</strong>
                  <code class="muted">{shortId(o.id)}</code>
                  {#if o.active}<span class="tag">active</span>{/if}
                  <span class="grow"></span>
                  <button type="button" class="small" onclick={() => (sides = { ...sides, right: o.id ?? MAIN_SCOPE })}>Compare with this</button>
                </div>
                {#if o.option?.hypothesis}<p class="hyp">{o.option.hypothesis}</p>{/if}
                {#if o.evaluation}<p class="hyp"><span class="muted">Evaluation:</span> {o.evaluation}</p>{/if}
              </li>
            {/each}
          </ul>
        {:else}
          <p class="hint">No option: open one from the scope bar (+ Option) to explore a hypothesis without touching the main flow of the change.</p>
        {/if}
      </details>
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 97;
    display: grid;
    place-items: center;
    background: rgb(0 0 0 / 0.4);
  }
  .panel {
    width: min(1180px, calc(100vw - 32px));
    height: min(860px, calc(100vh - 32px));
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
    outline: none;
  }
  .head {
    flex: none;
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 0.7rem 1rem;
    border-bottom: 1px solid var(--border);
    background: var(--chrome-2);
  }
  .title,
  .sides,
  .decide,
  .chips,
  .line,
  .oline {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .title h2 {
    margin: 0;
    font-size: 1.05em;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .level {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 0.85em;
    color: var(--muted);
  }
  .side {
    flex: 1;
    min-width: 14em;
    display: flex;
    align-items: center;
    gap: 6px;
    border-left: 5px solid var(--c);
    padding-left: 8px;
  }
  .side select {
    flex: 1;
    min-width: 0;
  }
  .cap {
    font-size: 0.8em;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
  }
  .swap {
    font-size: 1.1em;
  }
  .decide {
    border-left: 5px solid var(--c, var(--border));
    padding-left: 8px;
  }
  .decide input {
    flex: 1;
    min-width: 12em;
  }
  .eval {
    max-width: 40em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.8rem 1rem;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .chip {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 2px 10px;
    background: var(--surface);
    color: var(--text);
    cursor: pointer;
  }
  .chip .n {
    font-weight: 700;
    margin-left: 4px;
  }
  .chip.off {
    opacity: 0.5;
    text-decoration: line-through;
  }
  .chip.added,
  .group.added h4 {
    --k: var(--ok, #2e7d32);
  }
  .chip.removed,
  .group.removed h4 {
    --k: var(--danger, #c62828);
  }
  .chip.modified,
  .group.modified h4 {
    --k: var(--warn, #b26a00);
  }
  .chip:not(.off) {
    border-color: var(--k);
  }
  .group h4 {
    margin: 0 0 2px;
    color: var(--k);
  }
  .group p {
    margin: 0 0 4px;
  }
  .rows,
  .changes,
  .options {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .row {
    border: 1px solid var(--border);
    border-left: 4px solid var(--k, var(--border));
    border-radius: var(--radius-sm);
    padding: 6px 10px;
  }
  .row.added {
    --k: var(--ok, #2e7d32);
  }
  .row.removed {
    --k: var(--danger, #c62828);
  }
  .row.modified {
    --k: var(--warn, #b26a00);
  }
  .key {
    font-weight: 700;
  }
  .changes {
    margin-top: 6px;
    gap: 3px;
  }
  .change {
    display: flex;
    gap: 8px;
    align-items: baseline;
    flex-wrap: wrap;
  }
  .op {
    width: 1em;
    text-align: center;
    font-weight: 700;
  }
  .change.added .op {
    color: var(--ok, #2e7d32);
  }
  .change.removed .op {
    color: var(--danger, #c62828);
  }
  .change.changed .op {
    color: var(--warn, #b26a00);
  }
  .name {
    font-weight: 600;
  }
  .vals {
    display: inline-flex;
    gap: 6px;
    align-items: baseline;
    flex-wrap: wrap;
    min-width: 0;
  }
  .vals code {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .old {
    text-decoration: line-through;
    opacity: 0.8;
  }
  .identical {
    margin: 0;
  }
  .more {
    border-top: 1px solid var(--border);
    padding-top: 8px;
  }
  .options li {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 6px 10px;
  }
  .options li.current {
    border-color: var(--accent);
  }
  .hyp {
    margin: 4px 0 0;
  }
  .tag {
    font-size: 0.8em;
    color: var(--accent);
  }
  .danger {
    color: var(--danger);
    border-color: var(--danger);
  }
</style>
