<script lang="ts">
  // Single global right-click menu, mounted once at the shell root.
  import Icon from './Icon.svelte';
  import { menuState, closeContextMenu, type ContextMenuItem } from './contextMenuState.svelte';

  let el = $state<HTMLDivElement>();

  $effect(() => {
    if (!menuState.items.length) return;
    const down = (e: MouseEvent) => {
      if (el && !el.contains(e.target as Node)) closeContextMenu();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeContextMenu();
    };
    document.addEventListener('mousedown', down);
    document.addEventListener('keydown', key);
    return () => {
      document.removeEventListener('mousedown', down);
      document.removeEventListener('keydown', key);
    };
  });

  // Keep the menu inside the viewport.
  $effect(() => {
    if (!menuState.items.length || !el) return;
    const r = el.getBoundingClientRect();
    const x = Math.min(menuState.x, Math.max(4, window.innerWidth - r.width - 4));
    const y = Math.min(menuState.y, Math.max(4, window.innerHeight - r.height - 4));
    if (x !== menuState.x) menuState.x = x;
    if (y !== menuState.y) menuState.y = y;
  });

  function run(it: ContextMenuItem) {
    if (it.disabled) return;
    closeContextMenu();
    it.run();
  }
</script>

{#if menuState.items.length}
  <div class="menu" role="menu" bind:this={el} style:left="{menuState.x}px" style:top="{menuState.y}px">
    {#each menuState.items as it (it.label)}
      <button type="button" role="menuitem" class:danger={it.danger} disabled={it.disabled} onclick={() => run(it)}>
        {#if it.icon}<Icon name={it.icon} size={13} />{/if}
        <span>{it.label}</span>
      </button>
    {/each}
  </div>
{/if}

<style>
  .menu {
    position: fixed;
    z-index: 90;
    min-width: 160px;
    padding: 4px;
    display: grid;
    gap: 1px;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    font-size: 13px;
  }
  .menu button {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    width: 100%;
    padding: 0.3rem 0.5rem;
    border: none;
    background: transparent;
    color: inherit;
    text-align: left;
    border-radius: calc(var(--radius) - 2px);
    min-height: 0;
  }
  .menu button:hover:not(:disabled) {
    background: var(--hover);
  }
  .menu button:disabled {
    color: var(--muted);
    cursor: default;
  }
  .menu button.danger {
    color: var(--danger);
  }
</style>
