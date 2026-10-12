<script lang="ts">
  // A change against the head of the branch it was forked from: main or a release branch for a root change (ADR 0101),
  // the branch of its parent change for a sub-change (ADR 0103). Rebase brings its drafts up to date, and the conflicts a rebase left are listed
  // (an edit of the field settles one, "Keep mine" keeps the change's values for all of an impact's). Apply rebases by
  // itself; this pane says what stands in its way.
  import { graph, errorMessage, type Change, type RebaseState } from '../api';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { notify } from '../shell/workbench.svelte';
  import { behindLine, conflictLabel, syncVisible } from '../rebase';

  let { change, closed = false, onchange }: { change: Change; closed?: boolean; onchange?: () => void } = $props();

  let sync = $state<RebaseState | undefined>();
  let busy = $state('');
  let error = $state('');

  const id = $derived(change.id ?? '');
  const behind = $derived(sync?.behindImpactIds ?? []);
  const conflicts = $derived(sync?.conflicts ?? []);
  const isSub = $derived(!!change.parentId);
  // a change that is committed (waiting for its three-way merge), applied or abandoned is integrated or closed, never rebased
  const live = $derived(!['committed', 'applied', 'abandoned'].includes(change.status ?? ''));
  const onto = $derived(sync?.onto ?? '');
  const target = $derived(sync?.branch || (isSub ? "the parent's branch" : 'the target branch'));
  const keyOfImpact = (impact: string) => (change.nodes ?? []).find((n) => n.id === impact)?.key ?? impact;

  $effect(() => {
    const c = id;
    if (!c) return;
    void change.status; // a failed or finished apply changes the status: read the state again
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
        touched ? `Rebased: ${touched} impact${touched > 1 ? 's' : ''} to review again${conflicting ? `, ${conflicting} with conflicts to settle` : ''}.` : 'Up to date.',
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

{#if live && syncVisible({ onto, conflicts: conflicts.length, error })}
  <section class="sync" aria-label={isSub ? 'Sub-change and the branch of its parent' : 'Change and its target branch'}>
    {#if onto}
      <div class="row">
        <span>{behindLine(sync?.branch ?? '', onto, behind.map(keyOfImpact))}</span>
        {#if !closed}
          <button type="button" onclick={rebase} disabled={busy !== ''} title="Merge what landed on the branch into this change's drafts; the fields changed on both sides become conflicts">
            {busy === 'rebase' ? 'Rebasing…' : 'Rebase'}
          </button>
        {/if}
      </div>
    {/if}
    {#if conflicts.length}
      <h4>Conflicts with {target}</h4>
      <ul>
        {#each conflicts as c (c.changeImpactId)}
          <li>
            <strong>{c.key}</strong>:
            {(c.conflicts ?? []).map(conflictLabel).join(', ')}
            {#if !closed}
              <button type="button" class="link" onclick={() => keepMine(c.changeImpactId ?? '')} disabled={busy !== ''} title="Keep this change's values for these fields (edit a field to settle it with another value)">
                {busy === c.changeImpactId ? 'Keeping…' : 'Keep mine'}
              </button>
            {/if}
          </li>
        {/each}
      </ul>
      <p class="hint">Edit a field to settle its conflict with another value; cancel the draft to take the node of {target}. A settled impact is reviewed again.</p>
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
