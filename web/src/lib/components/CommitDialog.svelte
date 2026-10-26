<script lang="ts">
  // The Commit button's dialog: the nodes the next commit may write (the candidates of graph.getCommitState), each to
  // tick, all ticked at first; the commit writes the ones ticked (a partial commit) and the change stays open, the others
  // left for a later commit. The server judges the choice (a version linking to the draft of a node left out is refused).
  import type { CommitCandidate } from '../api';
  import { candidateLabel, commitSelection } from '../changeCommit';
  import Icon from '../shell/Icon.svelte';

  let {
    candidates,
    branch,
    busy = false,
    error = '',
    oncommit,
    onclose,
  }: {
    candidates: CommitCandidate[];
    /** the branch the commit writes on */
    branch: string;
    busy?: boolean;
    error?: string;
    /** commit the nodes named; none named: every candidate */
    oncommit: (nodes: string[]) => void;
    onclose: () => void;
  } = $props();

  let chosen = $state(new Set<string>());
  $effect(() => {
    chosen = new Set(candidates.map((c) => c.nodeId ?? '').filter(Boolean));
  });

  const selection = $derived(commitSelection(candidates, chosen));
  const all = $derived(chosen.size === candidates.length);

  function toggle(id: string) {
    const next = new Set(chosen);
    if (!next.delete(id)) next.add(id);
    chosen = next;
  }

  function toggleAll() {
    chosen = all ? new Set() : new Set(candidates.map((c) => c.nodeId ?? '').filter(Boolean));
  }

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (selection && !busy) oncommit(selection.nodes);
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && !busy && onclose()} />

<div class="backdrop" role="presentation" onmousedown={() => !busy && onclose()}>
  <form class="panel" role="dialog" aria-modal="true" aria-label="Commit the change" tabindex="-1" onmousedown={(e) => e.stopPropagation()} onsubmit={submit}>
    <header class="head">
      <h2>Commit on <code>{branch}</code></h2>
      <span class="grow"></span>
      <button type="button" class="ghost small" aria-label="Close" onclick={onclose} disabled={busy}><Icon name="x" size={14} /></button>
    </header>
    <div class="body">
      <p class="hint">The nodes ticked are written as new versions on the branch of the change. The others stay drafts, for a later commit; the change stays open.</p>
      <label class="all">
        <input type="checkbox" checked={all} indeterminate={!all && chosen.size > 0} onchange={toggleAll} disabled={busy} />
        {chosen.size} of {candidates.length} node(s)
      </label>
      <ul class="nodes">
        {#each candidates as c (c.nodeId)}
          <li>
            <label>
              <input type="checkbox" checked={chosen.has(c.nodeId ?? '')} onchange={() => toggle(c.nodeId ?? '')} disabled={busy} />
              <code>{c.key}</code>
              <span class="muted">{c.type}</span>
              <span class="kind kind-{c.kind}">{candidateLabel(c.kind)}</span>
            </label>
          </li>
        {/each}
      </ul>
      {#if error}<div class="alert small">{error}</div>{/if}
    </div>
    <footer class="foot">
      <button type="button" onclick={onclose} disabled={busy}>Cancel</button>
      <button type="submit" class="primary" disabled={busy || !selection}>
        {busy ? 'Working…' : all ? 'Commit all' : `Commit ${chosen.size} node(s)`}
      </button>
    </footer>
  </form>
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
    width: min(620px, calc(100vw - 32px));
    max-height: min(680px, calc(100vh - 32px));
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
  .head,
  .foot {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0.7rem 1rem;
    background: var(--chrome-2);
  }
  .head {
    border-bottom: 1px solid var(--border);
  }
  .foot {
    justify-content: flex-end;
    border-top: 1px solid var(--border);
  }
  .head h2 {
    margin: 0;
    font-size: 1.05em;
  }
  .grow {
    flex: 1;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.8rem 1rem;
  }
  .hint {
    margin: 0 0 0.6rem;
    font-size: 0.88em;
    color: var(--muted);
  }
  .all {
    display: flex;
    align-items: center;
    gap: 6px;
    font-weight: 600;
    padding-bottom: 0.4rem;
    border-bottom: 1px solid var(--border);
  }
  .nodes {
    list-style: none;
    margin: 0.4rem 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .nodes label {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .muted {
    color: var(--muted);
    font-size: 0.85em;
  }
  .kind {
    margin-left: auto;
    font-size: 0.8em;
    color: var(--muted);
  }
  .kind-reverted {
    color: var(--warn, var(--muted));
  }
  .alert.small {
    margin-top: 0.6rem;
    font-size: 0.88em;
  }
</style>
