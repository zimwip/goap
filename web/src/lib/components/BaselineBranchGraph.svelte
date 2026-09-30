<script lang="ts">
  // History of one branch: the baseline chain of a single branch (git log --graph, one lane), plus a single anchor
  // dot where it forks from (or merges with) another branch — the only two moments a second lane is informative
  // (ADR 0032). Unlike a namespace-wide graph, a branch never runs as a parallel lane for its whole life here.
  import type { Baseline } from '../api';
  import { formatDate, shortId } from '../api';
  import { MAIN_BRANCH, isEphemeralBranch } from '../namespace';
  import { changes, refreshChanges } from '../stores/catalog.svelte';
  import HistoryGraph, { type LaneEntry } from './HistoryGraph.svelte';

  if (!changes.loaded) void refreshChanges();

  let {
    baselines,
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

  const branchOf = (b: Baseline) => b.branch || MAIN_BRANCH;
  const changeLabel = (b: Baseline) => (b.changeId ? `change ${b.changeId.slice(0, 8)}` : undefined);
  // the baseline's name is a user-editable override of the change title at apply time (ChangeTab "New baseline
  // name"): show the change's own title so a renamed baseline still reads as what produced it.
  const titleOf = (b: Baseline) =>
    (b.changeId && changes.items.find((c) => c.id === b.changeId)?.title) || b.name || shortId(b.id);

  const own = $derived(baselines.filter((b) => branchOf(b) === branch));
  const byId = $derived(new Map(baselines.map((b) => [b.id, b])));

  /** a merge from a change's own scratch branch (ADR 0032): plumbing, not a branch worth showing as its own row */
  const ephemeralSource = (b: Baseline) => {
    const src = b.mergedFrom && byId.get(b.mergedFrom);
    return !!src && isEphemeralBranch(branchOf(src));
  };
  /** the change's own starting baseline, when it differs from the plain previous head (a real 3-way merge that
   * needed rework, not just the structural wrapper every change's own branch uses to land) */
  const startOf = (b: Baseline) => {
    if (!ephemeralSource(b)) return undefined;
    const start = b.changeId && changes.items.find((c) => c.id === b.changeId)?.baselineId;
    return start && start !== b.parentId ? start : undefined;
  };

  /** a parent/mergedFrom id that isn't itself on this branch: the branch's fork point, or a (named-branch) merge source */
  const ghosts = $derived.by(() => {
    const ownIds = new Set(own.map((b) => b.id));
    const ids = new Set<string>();
    if (forkBaseline && !ownIds.has(forkBaseline)) ids.add(forkBaseline);
    for (const b of own) {
      if (b.parentId && !ownIds.has(b.parentId)) ids.add(b.parentId);
      if (b.mergedFrom && !ownIds.has(b.mergedFrom) && !ephemeralSource(b)) ids.add(b.mergedFrom);
    }
    return [...ids].map((id) => byId.get(id)).filter((b): b is Baseline => !!b);
  });

  const entries = $derived<LaneEntry<Baseline>[]>(
    [
      ...own.map((h) => {
        const start = startOf(h);
        // a branch merged back with no changes of its own has parentId === mergedFrom (or === start): dedupe by
        // id so the same edge is never listed twice (a keyed each block would throw on the duplicate key)
        const seen = new Set<string>();
        const parents = [
          ...(h.parentId ? [{ id: h.parentId, label: changeLabel(h) }] : []),
          ...(h.mergedFrom && !ephemeralSource(h) ? [{ id: h.mergedFrom, label: changeLabel(h) }] : []),
          ...(start ? [{ id: start, label: changeLabel(h), dashed: true }] : []),
        ].filter((p) => (seen.has(p.id) ? false : (seen.add(p.id), true)));
        return {
          id: h.id ?? '',
          // what descends a baseline from another is the change that produced it: the edge, not the dot
          parents,
          lane: branch,
          item: h,
        };
      }),
      ...ghosts.map((h) => ({ id: h.id ?? '', parents: [], lane: branchOf(h), item: h })),
    ].sort((a, b) => (b.item.createdAt ?? '').localeCompare(a.item.createdAt ?? '')),
  );
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
          </span>
          <span class="hline2">
            {#if h.changeId}<span class="hchange">change {h.changeId.slice(0, 8)}</span>{/if}
            <span class="hdate">{formatDate(h.createdAt)}</span>
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
  .empty.small {
    font-size: 0.85em;
  }
</style>
