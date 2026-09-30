<script lang="ts">
  // Branch graph of a node's versions, like `git log --graph`: a thin adaptation of the agnostic HistoryGraph to
  // GraphNode versions (see HistoryGraph.svelte for the graph-drawing itself).
  import type { GraphNode } from '../api';
  import { MAIN_BRANCH, isEphemeralBranch } from '../namespace';
  import HistoryGraph, { type LaneEntry } from './HistoryGraph.svelte';

  let {
    versions,
    selected,
    onselect,
    describe,
  }: {
    /** the versions of the node, any order */
    versions: GraphNode[];
    selected?: number;
    onselect?: (version: number) => void;
    /** text of a row after the version and its branch (reason, state, change…) */
    describe: (v: GraphNode) => string;
  } = $props();

  const branchOf = (v: GraphNode) => v.branch || MAIN_BRANCH;

  /** parents of a version (legacy versions without parents descend from the previous one, as pkg/graph) */
  const parentsOf = (v: GraphNode): number[] =>
    v.parents?.length ? v.parents : (v.version ?? 0) > 1 ? [(v.version ?? 0) - 1] : [];

  const entries = $derived<LaneEntry<GraphNode>[]>(
    [...versions]
      .sort((a, b) => (b.version ?? 0) - (a.version ?? 0))
      .map((v) => ({
        id: String(v.version ?? 0),
        parents: parentsOf(v).map((pv) => ({ id: String(pv) })),
        lane: branchOf(v),
        deleted: v.deleted,
        ephemeral: isEphemeralBranch(branchOf(v)),
        item: v,
      })),
  );
</script>

<HistoryGraph {entries} selected={selected !== undefined ? String(selected) : undefined} mainLane={MAIN_BRANCH} onselect={(id) => onselect?.(Number(id))}>
  {#snippet row({ entry, head, color })}
    {@const v = entry.item as GraphNode}
    <strong>v{v.version}</strong>
    {#if head}<span class="ref" style={`--c:${color}`}>{entry.lane}</span>{/if}
    {#if v.state}<span class="state">{v.state}</span>{/if}
    <span class="desc">{describe(v)}</span>
  {/snippet}
</HistoryGraph>

<style>
  .ref {
    border: 1px solid var(--c);
    color: var(--c);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.78rem;
    font-family: var(--mono);
  }
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.78rem;
    font-family: var(--mono);
  }
  .desc {
    color: var(--muted);
    font-size: 0.88rem;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
