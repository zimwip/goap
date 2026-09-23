<script lang="ts">
  import { toasts, dismiss } from './workbench.svelte';
</script>

<div class="toasts" aria-live="polite">
  {#each toasts as t (t.id)}
    <div class="toast {t.tone}" role="status">
      <span>{t.text}</span>
      <button type="button" class="ghost small" aria-label="Fermer" onclick={() => dismiss(t.id)}>✕</button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    right: 14px;
    bottom: 32px;
    z-index: 100;
    display: grid;
    gap: 6px;
    max-width: min(420px, calc(100vw - 28px));
  }
  .toast {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.45rem 0.4rem 0.45rem 0.75rem;
    border-radius: var(--radius);
    background: var(--surface);
    border: 1px solid var(--border);
    border-left: 3px solid var(--info);
    box-shadow: var(--shadow-pop);
  }
  .toast span {
    flex: 1;
  }
  .ok {
    border-left-color: var(--ok);
  }
  .error {
    border-left-color: var(--danger);
    color: var(--danger);
  }
</style>
