<script lang="ts">
  // History of one branch (git log --graph): its own commits in one lane, and a lane for the branch of each change
  // that committed on it, from where the change started to the commit the merge joined (commitHistory.ts), so two
  // changes started from the same commit show as two lanes forking from it. Other branches appear only as the dot
  // where this one forks from or merges with them.
  import type { Commit, Branch } from '../api';
  import { formatDate, shortId } from '../api';
  import { badgesOf, branchLabel, countsOf, historyEntries, matchesCommit, OPEN_KIND } from '../commitHistory';
  import { changes, refreshChanges } from '../stores/catalog.svelte';
  import HistoryGraph from './HistoryGraph.svelte';

  if (!changes.loaded) void refreshChanges();

  let {
    commits,
    branches = [],
    branch,
    forkCommit,
    selected,
    rowHeight = 58,
    wrap = true,
    onselect,
    onopen,
    oncontextmenu,
    filter = '',
    lanes = true,
  }: {
    /** every commit of the namespace, any order */
    commits: Commit[];
    /** the branches of the namespace: the open ones forked from `branch` get a lane, committed or not (ADR 0102) */
    branches?: Branch[];
    /** the branch whose own activity to show */
    branch: string;
    /** the branch's fork point (Branch.forkCommit): anchors the graph even before the branch has a commit of its own */
    forkCommit?: string;
    selected?: string;
    rowHeight?: number;
    wrap?: boolean;
    onselect?: (id: string) => void;
    onopen?: (id: string) => void;
    /** right click on a row */
    oncontextmenu?: (id: string, e: MouseEvent) => void;
    /** the text typed in the history's filter: the rows not matching it are dimmed, the graph kept whole */
    filter?: string;
    /** false: the branch's own commits only, the lanes of the changes and of the open branches folded away */
    lanes?: boolean;
  } = $props();

  const changeLabel = (b: Commit) => (b.changeId ? `change ${b.changeId.slice(0, 8)}` : undefined);
  // the commit's name is a user-editable override of the change title at apply time (ChangeTab "New commit
  // name"): show the change's own title so a renamed commit still reads as what produced it.
  const titleOf = (b: Commit) =>
    (b.changeId && changes.items.find((c) => c.id === b.changeId)?.title) || b.name || shortId(b.id);
  const isOpen = (b: Commit) => b.kind === OPEN_KIND;

  const kindTitle: Record<string, string> = {
    commit: 'the versions of a change, written on its own branch',
    'fast-forward': 'the target branch moved to the commit of a change (linear history)',
    merge: 'a three-way merge of a branch',
    rebase: 'the branch of a change moved onto the head of its target before its merge',
    open: 'a branch forked from here, still open',
  };
  const badges = (b: Commit) => badgesOf(b, commits);

  const entries = $derived(historyEntries(commits, branch, forkCommit, changeLabel, branches, lanes));
  const dimmed = (h: Commit) => !!filter.trim() && !matchesCommit(h, filter, titleOf(h));
  /** the counters of the change a commit of the branch landed: on the row where it joined the branch */
  const countsAt = (h: Commit) =>
    h.changeId && (h.branch || 'main') === branch && ['fast-forward', 'merge'].includes(badges(h).kind) ? countsOf(changes.items.find((c) => c.id === h.changeId)?.stats) : undefined;
</script>

{#if entries.length}
  <div class="hscroll">
    <HistoryGraph {entries} {selected} mainLane={branch} {rowHeight} {wrap} {onselect} {onopen} {oncontextmenu}>
      {#snippet row({ entry, head, color })}
        {@const h = entry.item as Commit}
        {@const counts = countsAt(h)}
        <span class="hrow" class:dim={dimmed(h)}>
          <span class="hline1">
            <span class="hname">{titleOf(h)}</span>
            {#if head}<span class="ref" style={`--c:${color}`} title={entry.lane}>{branchLabel(entry.lane)}</span>{/if}
            {#if badges(h).kind !== 'snapshot'}<span class="kind" data-kind={badges(h).kind} title={kindTitle[badges(h).kind]}>{badges(h).kind}</span>{/if}
            {#if badges(h).rebased}<span class="kind rebased" title={badges(h).rebased}>rebased</span>{/if}
          </span>
          <span class="hline2">
            {#if isOpen(h)}<span class="hnote">{h.branch}: open</span>{/if}
            {#if h.changeId}<span class="hchange">change {h.changeId.slice(0, 8)}</span>{/if}
            {#if counts}<span class="counts" title={counts.title}>{counts.text}</span>{/if}
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
    overflow-x: hidden;
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
  .hrow.dim {
    opacity: 0.35;
  }
  .counts {
    font-size: 0.78em;
    font-family: var(--mono);
    font-variant-numeric: tabular-nums;
    color: var(--text-2, var(--muted));
    flex: none;
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
    max-width: 14em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
