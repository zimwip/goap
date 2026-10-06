<script lang="ts">
  // History of a change with its parent and its sub-changes (ADR 0081, 0082), as a branch graph: a lane per change, a row
  // per event of its impact log, the parent's lane on the left. A sub-change copying a draft of its parent forks from the
  // parent's event it copied, a rebase ties back to the parent's event it caught up with, an integration merges the
  // sub-change's lane into the parent's. A node filter follows one node across the family.
  import { graph, errorMessage, formatDate, type Change, type LogEntry } from '../api';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import HistoryGraph from './HistoryGraph.svelte';
  import { familyEntries, familyKeys, type FamilyMember, type HistoryItem } from '../subChangeHistory';

  let { change, parent, subs }: { change: Change; parent?: Change; subs: Change[] } = $props();

  const members = $derived<FamilyMember[]>(
    [...(parent ? [parent] : []), change, ...subs].map((c) => ({ id: c.id ?? '', title: c.title, parentId: c.parentId })),
  );
  let logs = $state(new Map<string, LogEntry[]>());
  let error = $state('');
  let node = $state('');
  let selected = $state<string | undefined>();

  $effect(() => {
    const ids = members.map((m) => m.id).filter(Boolean);
    for (const id of ids) stamp(keyOf.change(id)); // any of them moved: read again
    const ctrl = new AbortController();
    Promise.all(ids.map((id) => graph.listChangeLog({ changeId: id, types: ['impact.'] }, ctrl.signal).then((r) => [id, r.entries ?? []] as const)))
      .then((all) => {
        logs = new Map(all);
        error = '';
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      });
    return () => ctrl.abort();
  });

  const all = $derived(familyEntries(members, logs));
  const keys = $derived(familyKeys(all));
  const entries = $derived(node ? familyEntries(members, logs, node) : all);
  const picked = $derived(entries.find((e) => e.id === selected)?.item);
  const mainLane = $derived(members[0]?.id ?? '');
</script>

<div class="head">
  <label>
    Node
    <select bind:value={node}>
      <option value="">every node</option>
      {#each keys as k (k)}<option value={k}>{k}</option>{/each}
    </select>
  </label>
  <span class="hint">a lane per change, the {parent ? 'parent' : 'change'} on the left</span>
</div>
{#if error}<div class="error">{error}</div>{/if}
{#if entries.length}
  <div class="hscroll">
    <HistoryGraph {entries} {selected} {mainLane} rowHeight={46} wrap onselect={(id) => (selected = id)} onopen={(id) => openTab({ kind: 'change', params: { id: id.split(':')[0] } })}>
      {#snippet row({ entry, head, color })}
        {@const h = entry.item as HistoryItem}
        <span class="hrow">
          <span class="hline1">
            <strong>{h.key}</strong>
            <span class="label {h.kind}">{h.label}</span>
          </span>
          <span class="hline2">
            {#if head}<span class="ref" style={`--c:${color}`}>{h.changeTitle}</span>{:else}<span class="hchange">{h.changeTitle}</span>{/if}
            <span class="hdate">{formatDate(h.at)}</span>
          </span>
        </span>
      {/snippet}
    </HistoryGraph>
  </div>
  {#if picked}
    <p class="detail">
      <strong>{picked.key}</strong> · {picked.label} · in <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: picked.change } })}>{picked.changeTitle}</button>
      {#if picked.by}· by {picked.by}{/if} · {formatDate(picked.at)}
    </p>
  {/if}
{:else if !error}
  <p class="empty small">No history.</p>
{/if}

<style>
  .head {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 0.5rem;
  }
  .head select {
    width: auto;
    margin-left: 0.35rem;
  }
  .hint {
    color: var(--muted);
    font-size: 0.85em;
  }
  .hscroll {
    max-height: 28rem;
    overflow-y: auto;
  }
  .hrow {
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-width: 0;
    flex: 1;
  }
  .hline1,
  .hline2 {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    min-width: 0;
  }
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .label.rebase,
  .label.fork {
    color: var(--warn);
  }
  .label.integrate,
  .label.land {
    color: var(--ok, var(--accent));
  }
  .hchange {
    color: var(--muted);
    font-size: 0.8em;
  }
  .hdate {
    color: var(--muted);
    font-size: 0.8em;
    margin-left: auto;
  }
  .ref {
    border: 1px solid var(--c);
    color: var(--c);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.78rem;
    flex: none;
  }
  .detail {
    margin: 0.5rem 0 0;
    font-size: 0.9em;
  }
  .empty.small {
    font-size: 0.85em;
  }
</style>
