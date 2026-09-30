<script lang="ts">
  // Single global small-form dialog, mounted once at the shell root: same shell as ObjectDialog.svelte, generic
  // over whatever fields the caller (a context menu action, usually) asks for.
  import { fieldDialog, closeFieldDialog } from './fieldDialogState.svelte';
  import { errorMessage } from '../api';

  let values = $state<Record<string, string>>({});
  let busy = $state(false);
  let error = $state('');

  $effect(() => {
    if (!fieldDialog.open) return;
    values = Object.fromEntries(fieldDialog.fields.map((f) => [f.key, f.default ?? '']));
    error = '';
    busy = false;
  });

  /** focuses the element it's attached to, once, when it mounts, when `on` is true (the dialog's first field) */
  function autofocus(el: HTMLElement, on: boolean) {
    if (on) queueMicrotask(() => el.focus());
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    for (const f of fieldDialog.fields) {
      if (f.required && !values[f.key]?.trim()) {
        error = `${f.label} is required.`;
        return;
      }
    }
    busy = true;
    error = '';
    try {
      await fieldDialog.onsubmit?.(values);
      closeFieldDialog();
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:window onkeydown={(e) => fieldDialog.open && e.key === 'Escape' && closeFieldDialog()} />

{#if fieldDialog.open}
  <div class="backdrop" role="presentation" onmousedown={closeFieldDialog}>
    <div class="dialog" role="dialog" aria-label={fieldDialog.title} tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
      <form onsubmit={submit}>
        <h3>{fieldDialog.title}</h3>
        {#each fieldDialog.fields as f, i (f.key)}
          <div class="field">
            <label for="fd-{f.key}">{f.label}</label>
            {#if f.type === 'textarea'}
              <textarea id="fd-{f.key}" bind:value={values[f.key]} placeholder={f.placeholder} use:autofocus={i === 0}></textarea>
            {:else}
              <input id="fd-{f.key}" type="text" bind:value={values[f.key]} placeholder={f.placeholder} use:autofocus={i === 0} />
            {/if}
          </div>
        {/each}
        {#if error}<div class="alert small">{error}</div>{/if}
        <div class="row">
          <button type="button" class="ghost" onclick={closeFieldDialog}>Cancel</button>
          <button type="submit" class="primary" disabled={busy}>{busy ? `${fieldDialog.submitLabel}…` : fieldDialog.submitLabel}</button>
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
