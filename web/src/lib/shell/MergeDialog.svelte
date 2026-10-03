<script lang="ts">
  // Single global merge dialog, mounted once at the shell root: hosts MergeResolver (preview + per-node conflict
  // resolution), opened from a branch's context menu instead of an inline form.
  import { mergeDialog, closeMergeDialog } from './mergeDialogState.svelte';
  import { graph, shortId, type Resolution } from '../api';
  import { notify } from './workbench.svelte';
  import MergeResolver from '../components/MergeResolver.svelte';

  async function merge(resolutions: Record<string, Resolution>): Promise<boolean> {
    const r = await graph.mergeBranch({ namespace: mergeDialog.namespace, from: mergeDialog.from, into: mergeDialog.into, resolutions });
    notify(`${mergeDialog.from} merged into ${mergeDialog.into}: baseline ${r.baseline?.name || shortId(r.baseline?.id)}.`, 'ok');
    closeMergeDialog();
    return true;
  }
</script>

<svelte:window onkeydown={(e) => mergeDialog.open && e.key === 'Escape' && closeMergeDialog()} />

{#if mergeDialog.open}
  <div class="backdrop" role="presentation" onmousedown={closeMergeDialog}>
    <div class="dialog" role="dialog" aria-label="Merge a branch" tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
      <h3>Merge <code>{mergeDialog.from}</code> into <code>{mergeDialog.into}</code></h3>
      {#key `${mergeDialog.from}>${mergeDialog.into}`}
        <MergeResolver namespace={mergeDialog.namespace} from={mergeDialog.from} into={mergeDialog.into} onmerge={merge} />
      {/key}
      <div class="row">
        <button type="button" class="ghost" onclick={closeMergeDialog}>Cancel</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 95;
    display: grid;
    place-items: center;
    background: rgba(0, 0, 0, 0.4);
  }
  .dialog {
    width: min(640px, calc(100vw - 28px));
    max-height: calc(100vh - 40px);
    overflow: auto;
    display: grid;
    gap: 0.6rem;
    padding: 1rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  h3 {
    margin: 0;
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: 0.4rem;
  }
</style>
