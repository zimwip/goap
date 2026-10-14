<script lang="ts">
  // The Diff of one change impact (the Diff button of the Impacts pane): what the change modifies of the node, its
  // draft (or the version its landing wrote) against the version it was checked out from. Logic in `impactDiff.ts`.
  import { untrack } from 'svelte';
  import { errorMessage, isDraft, type ChangeImpact } from '../api';
  import { clip, describeChange } from '../flowDiff';
  import { loadImpactDiff, type ImpactDiffView } from '../impactDiff';
  import Icon from '../shell/Icon.svelte';

  let {
    changeId,
    impact,
    nodeKey,
    nodeType,
    flow = '',
    onclose,
  }: {
    changeId: string;
    impact: ChangeImpact;
    nodeKey: string;
    nodeType: string;
    /** the flow the impact is seen from ('main' or an option) */
    flow?: string;
    onclose?: () => void;
  } = $props();

  let view = $state<ImpactDiffView | undefined>();
  let error = $state('');
  let expanded = $state<ReadonlySet<number>>(new Set());

  $effect(() => {
    const ctrl = new AbortController();
    untrack(() => {
      view = undefined;
      error = '';
    });
    loadImpactDiff(changeId, impact, flow, ctrl.signal)
      .then((v) => (view = v))
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      });
    return () => ctrl.abort();
  });

  const side = (n: { version?: number } | undefined, draft: boolean) => (!n ? '—' : draft ? 'draft' : `v${n.version ?? 0}`);
  const afterLabel = $derived(impact.landed?.id ? `landed v${impact.landed.version}` : side(view?.after, isDraft(impact.post)));

  function toggle(i: number) {
    const next = new Set(expanded);
    if (!next.delete(i)) next.add(i);
    expanded = next;
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose?.()} />

<div class="backdrop" role="presentation" onmousedown={() => onclose?.()}>
  <div class="panel" role="dialog" aria-modal="true" aria-label="Diff of {nodeKey}" tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
    <header class="head">
      <h2>Diff <code>{nodeKey}</code> <span class="muted">{nodeType}</span></h2>
      <span class="grow"></span>
      <span class="sides" title="the version the change started from → what the change holds">{view?.base ? side(view.base, false) : 'new node'} → {afterLabel}</span>
      <button type="button" class="ghost small" aria-label="Close" onclick={() => onclose?.()}><Icon name="x" size={14} /></button>
    </header>
    <div class="body">
      {#if error}
        <div class="alert">{error}</div>
      {:else if !view}
        <p class="hint">Comparing…</p>
      {:else if !view.after}
        <p class="hint">Planned: the change holds no draft of this node yet, so it modifies nothing so far.</p>
      {:else if !view.changes.length}
        <p class="hint">The change holds the node as it was checked out: it modifies nothing.</p>
      {:else}
        <ul class="changes">
          {#each view.changes as c, i (i)}
            {@const l = describeChange(c)}
            {@const o = clip(l.old ?? '')}
            {@const n = clip(l.new ?? '')}
            {@const open = expanded.has(i)}
            <li class="change {l.op}">
              <span class="op" aria-label={l.op}>{l.op === 'added' ? '+' : l.op === 'removed' ? '−' : '~'}</span>
              <span class="name">{l.label}</span>
              {#if l.old !== undefined || l.new !== undefined}
                <span class="vals">
                  {#if l.old !== undefined}<code class="old">{open ? l.old : o.text}</code>{/if}
                  {#if l.old !== undefined && l.new !== undefined}<span class="arrow">→</span>{/if}
                  {#if l.new !== undefined}<code class="new">{open ? l.new : n.text}</code>{/if}
                  {#if o.clipped || n.clipped}<button type="button" class="link small" onclick={() => toggle(i)}>{open ? 'less' : 'more'}</button>{/if}
                </span>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
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
    width: min(760px, calc(100vw - 32px));
    max-height: min(720px, calc(100vh - 32px));
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
    align-items: center;
    gap: 8px;
    padding: 0.7rem 1rem;
    border-bottom: 1px solid var(--border);
    background: var(--chrome-2);
  }
  .head h2 {
    margin: 0;
    font-size: 1.05em;
  }
  .grow {
    flex: 1;
  }
  .sides {
    font-family: var(--mono, monospace);
    font-size: 0.85em;
    color: var(--muted);
  }
  .muted {
    color: var(--muted);
    font-weight: normal;
    font-size: 0.85em;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.8rem 1rem;
  }
  .changes {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
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
</style>
