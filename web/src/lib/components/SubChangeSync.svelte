<script lang="ts">
  // A sub-change against its parent (ADR 0082): the impacts whose draft the parent changed since the sub-change took it
  // (Rebase brings them up to date), and the conflicts a rebase left (an edit of the field settles one, "Keep mine" keeps
  // the sub-change's values for all of an impact's). Apply rebases by itself; this pane says what stands in its way.
  import { graph, errorMessage, type Change, type RebaseState } from '../api';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { notify } from '../shell/workbench.svelte';
  import { conflictLabel } from '../rebase';

  let { change, closed = false, onchange }: { change: Change; closed?: boolean; onchange?: () => void } = $props();

  let sync = $state<RebaseState | undefined>();
  let busy = $state('');
  let error = $state('');

  const id = $derived(change.id ?? '');
  const behind = $derived(sync?.behindImpactIds ?? []);
  const conflicts = $derived(sync?.conflicts ?? []);
  const keyOfImpact = (impact: string) => (change.nodes ?? []).find((n) => n.id === impact)?.key ?? impact;

  $effect(() => {
    const c = id;
    if (!c || !change.parentId) return;
    stamp(keyOf.change(c)); // the change moved: read its state again
    const ctrl = new AbortController();
    graph
      .getRebaseState(c, ctrl.signal)
      .then((s) => (sync = s))
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      });
    return () => ctrl.abort();
  });

  async function rebase() {
    busy = 'rebase';
    error = '';
    try {
      const r = await graph.rebaseChange(id);
      const touched = (r.impacts ?? []).filter((i) => i.changed || i.conflicts?.length).length;
      const conflicting = (r.impacts ?? []).filter((i) => i.conflicts?.length).length;
      notify(
        touched ? `Rebased: ${touched} impact${touched > 1 ? 's' : ''} to review again${conflicting ? `, ${conflicting} with conflicts to settle` : ''}.` : 'Up to date with the parent.',
        conflicting ? 'info' : 'ok',
      );
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  async function keepMine(impact: string) {
    busy = impact;
    error = '';
    try {
      await graph.impactNodeResolve(id, impact);
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }
</script>

{#if change.parentId && (behind.length || conflicts.length || error)}
  <section class="sync" aria-label="Sub-change and its parent">
    {#if behind.length}
      <div class="row">
        <span>
          The parent changed {behind.length} node{behind.length > 1 ? 's' : ''} this sub-change works on since it took {behind.length > 1 ? 'them' : 'it'}
          ({behind.map(keyOfImpact).join(', ')}).
        </span>
        {#if !closed}
          <button type="button" onclick={rebase} disabled={busy !== ''} title="Merge the parent's changes into this sub-change's drafts; the fields changed on both sides become conflicts">
            {busy === 'rebase' ? 'Rebasing…' : 'Rebase'}
          </button>
        {/if}
      </div>
    {/if}
    {#if conflicts.length}
      <h4>Conflicts with the parent</h4>
      <ul>
        {#each conflicts as c (c.changeImpactId)}
          <li>
            <strong>{c.key}</strong>:
            {(c.conflicts ?? []).map(conflictLabel).join(', ')}
            {#if !closed}
              <button type="button" class="link" onclick={() => keepMine(c.changeImpactId ?? '')} disabled={busy !== ''} title="Keep this sub-change's values for these fields (edit a field to settle it with another value)">
                {busy === c.changeImpactId ? 'Keeping…' : 'Keep mine'}
              </button>
            {/if}
          </li>
        {/each}
      </ul>
      <p class="hint">Edit a field to settle its conflict with another value; cancel the draft to take the parent's node. A settled impact is reviewed again.</p>
    {/if}
    {#if error}<div class="error">{error}</div>{/if}
  </section>
{/if}

<style>
  .sync {
    border: 1px solid var(--warn);
    border-radius: 6px;
    padding: 0.5rem 0.75rem;
    margin: 0.75rem 0;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    justify-content: space-between;
  }
  h4 {
    margin: 0.5rem 0 0.25rem;
  }
  ul {
    margin: 0;
    padding-left: 1.25rem;
  }
  .hint {
    color: var(--muted);
    font-size: 0.85em;
    margin: 0.25rem 0 0;
  }
</style>
