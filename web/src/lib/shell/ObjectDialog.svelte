<script lang="ts">
  // Single global "New object" dialog, mounted once at the shell root.
  import { objectDialog, closeObjectDialog } from './objectDialogState.svelte';
  import { notify } from './workbench.svelte';
  import { graph, errorMessage, type Struct } from '../api';
  import { refreshBaselines } from '../stores/catalog.svelte';

  let key = $state('');
  let values = $state<Record<string, string>>({});
  let busy = $state(false);
  let error = $state('');
  let keyInput = $state<HTMLInputElement>();

  $effect(() => {
    if (!objectDialog.open) return;
    key = '';
    values = {};
    error = '';
    busy = false;
    queueMicrotask(() => keyInput?.focus());
  });

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!key.trim()) {
      error = 'The key is required.';
      return;
    }
    const props: Struct = {};
    for (const [k, v] of Object.entries(values)) if (v.trim()) props[k] = v.trim();
    busy = true;
    error = '';
    try {
      await graph.createObject(objectDialog.methodology, objectDialog.nodeType, key.trim(), props);
      notify(`${objectDialog.nodeType} ${key.trim()} created.`, 'ok');
      closeObjectDialog();
      void refreshBaselines();
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:window onkeydown={(e) => objectDialog.open && e.key === 'Escape' && closeObjectDialog()} />

{#if objectDialog.open}
  <div class="backdrop" role="presentation" onmousedown={closeObjectDialog}>
    <div class="dialog" role="dialog" aria-label="New object" tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
    <form onsubmit={create}>
      <h3>New {objectDialog.nodeType}</h3>
      <div class="field">
        <label for="obj-key">Key</label>
        <input id="obj-key" type="text" class="mono" bind:value={key} bind:this={keyInput} placeholder="REQ-10" />
      </div>
      {#each objectDialog.properties as p (p)}
        <div class="field">
          <label for="obj-{p}">{p}</label>
          <input id="obj-{p}" type="text" bind:value={values[p]} />
        </div>
      {/each}
      {#if error}<div class="alert small">{error}</div>{/if}
      <div class="row">
        <button type="button" class="ghost" onclick={closeObjectDialog}>Cancel</button>
        <button type="submit" class="primary" disabled={busy}>{busy ? 'Creating…' : 'Create'}</button>
      </div>
    </form>
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
    width: min(420px, calc(100vw - 28px));
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
  form {
    display: grid;
    gap: 0.6rem;
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
