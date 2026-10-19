<script lang="ts">
  // History of one branch (git log --graph): its own baselines in one lane, and a lane for the branch of each change
  // that landed on it, from where the change started to the commit the merge joined (baselineHistory.ts), so two
  // changes started from the same baseline show as two lanes forking from it. Other branches appear only as the dot
  // where this one forks from or merges with them.
  import type { Baseline, Branch } from '../api';
  import { formatDate, shortId } from '../api';
  import { badgesOf, historyEntries, OPEN_KIND } from '../baselineHistory';
  import { changes, refreshChanges } from '../stores/catalog.svelte';
  import HistoryGraph from './HistoryGraph.svelte';

  if (!changes.loaded) void refreshChanges();

  let {
    baselines,
    branches = [],
    branch,
    forkBaseline,
    selected,
    rowHeight = 58,
    wrap = true,
    onselect,
    onopen,
  }: {
    /** every baseline of the namespace, any order */
    baselines: Baseline[];
    /** the branches of the namespace: the open ones forked from `branch` get a lane, committed or not (ADR 0102) */
    branches?: Branch[];
    /** the branch whose own activity to show */
    branch: string;
    /** the branch's fork point (Branch.forkBaseline): anchors the graph even before the branch has a baseline of its own */
    forkBaseline?: string;
    selected?: string;
    rowHeight?: number;
    wrap?: boolean;
    onselect?: (id: string) => void;
    onopen?: (id: string) => void;
  } = $props();

  const changeLabel = (b: Baseline) => (b.changeId ? `change ${b.changeId.slice(0, 8)}` : undefined);
  // the baseline's name is a user-editable override of the change title at apply time (ChangeTab "New baseline
  // name"): show the change's own title so a renamed baseline still reads as what produced it.
  const titleOf = (b: Baseline) =>
    (b.changeId && changes.items.find((c) => c.id === b.changeId)?.title) || b.name || shortId(b.id);
  const isOpen = (b: Baseline) => b.kind === OPEN_KIND;

  const kindTitle: Record<string, string> = {
    commit: 'the versions of a change, written on its own branch',
    'fast-forward': 'the target branch moved to the commit of a change (linear history)',
    merge: 'a three-way merge of a branch',
    rebase: 'the branch of a change moved onto the head of its target before its merge',
    open: 'a branch forked from here, still open',
  };
  const badges = (b: Baseline) => badgesOf(b, baselines);

  const entries = $derived(historyEntries(baselines, branch, forkBaseline, changeLabel, branches));
</script>

{#if entries.length}
  <div class="hscroll">
    <HistoryGraph {entries} {selected} mainLane={branch} {rowHeight} {wrap} {onselect} {onopen}>
      {#snippet row({ entry, head, color })}
        {@const h = entry.item as Baseline}
        <span class="hrow">
          <span class="hline1">
            <span class="hname">{titleOf(h)}</span>
            {#if head}<span class="ref" style={`--c:${color}`}>{entry.lane}</span>{/if}
            {#if badges(h).kind !== 'snapshot'}<span class="kind" data-kind={badges(h).kind} title={kindTitle[badges(h).kind]}>{badges(h).kind}</span>{/if}
            {#if badges(h).rebased}<span class="kind rebased" title={badges(h).rebased}>rebased</span>{/if}
          </span>
          <span class="hline2">
            {#if isOpen(h)}<span class="hnote">{h.branch}: open</span>{/if}
            {#if h.changeId}<span class="hchange">change {h.changeId.slice(0, 8)}</span>{/if}
            {#if badges(h).rebased}<span class="hnote">{badges(h).rebased}</span>{/if}
            {#if badges(h).merged}<span class="hnote">{badges(h).merged}</span>{/if}
            {#if h.createdAt}<span class="hdate">{formatDate(h.createdAt)}</span>{/if}
          </span>
        </span>
      {/snippet}
    </HistoryGraph>
  </div>
{:else}
  <p class="empty small">No history.</p>
{/if}

<style>
  .hscroll {
    flex: 1;
    min-height: 0;
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
  .hname {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    word-break: break-word;
  }
  .hchange {
    color: var(--muted);
    font-size: 0.8em;
    font-family: var(--mono);
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
    font-family: var(--mono);
    flex: none;
  }
  .kind {
    border: 1px solid var(--muted);
    color: var(--muted);
    border-radius: 999px;
    padding: 0 0.4rem;
    font-size: 0.72rem;
    flex: none;
  }
  .kind.rebased {
    border-color: var(--warn);
    color: var(--warn);
  }
  .hnote {
    color: var(--muted);
    font-size: 0.78em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .empty.small {
    font-size: 0.85em;
  }
</style>
