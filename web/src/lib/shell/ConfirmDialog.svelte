<script lang="ts">
  // Single global confirmation dialog, mounted once at the shell root.
  import { confirmState, resolveConfirm } from './confirmState.svelte';

  let confirmBtn = $state<HTMLButtonElement>();

  $effect(() => {
    if (confirmState.current) queueMicrotask(() => confirmBtn?.focus());
  });
</script>

<svelte:window onkeydown={(e) => confirmState.current && e.key === 'Escape' && resolveConfirm(false)} />

{#if confirmState.current}
  {@const req = confirmState.current}
  <div class="backdrop" role="presentation" onmousedown={() => resolveConfirm(false)}>
    <div class="dialog" role="alertdialog" aria-modal="true" aria-label={req.title ?? 'Confirm'} tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
      {#if req.title}<h3>{req.title}</h3>{/if}
      <p>{req.message}</p>
      <div class="row">
        <button type="button" class="ghost" onclick={() => resolveConfirm(false)}>{req.cancelLabel ?? 'Cancel'}</button>
        <button
          type="button"
          class={req.danger ? 'danger' : 'primary'}
          bind:this={confirmBtn}
          onclick={() => resolveConfirm(true)}
        >
          {req.confirmLabel ?? 'OK'}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 98;
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
  h3 {
    margin: 0;
  }
  p {
    margin: 0;
    white-space: pre-wrap;
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: 0.4rem;
  }
</style>
