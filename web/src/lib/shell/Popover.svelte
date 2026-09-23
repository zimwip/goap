<script lang="ts">
  // Popup anchored to a button (status bar, header).
  import type { Snippet } from 'svelte';

  let {
    open = $bindable(false),
    label,
    align = 'left',
    placement = 'above',
    width = '320px',
    children,
  }: {
    open?: boolean;
    label: string;
    align?: 'left' | 'right';
    placement?: 'above' | 'below';
    width?: string;
    children: Snippet;
  } = $props();

  let el = $state<HTMLDivElement>();

  $effect(() => {
    if (!open) return;
    const down = (e: MouseEvent) => {
      const host = el?.parentElement;
      if (host && !host.contains(e.target as Node)) open = false;
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') open = false;
    };
    document.addEventListener('mousedown', down);
    document.addEventListener('keydown', key);
    return () => {
      document.removeEventListener('mousedown', down);
      document.removeEventListener('keydown', key);
    };
  });
</script>

{#if open}
  <div class="pop {align} {placement}" role="dialog" aria-label={label} bind:this={el} style:width>
    {@render children()}
  </div>
{/if}

<style>
  .pop {
    position: absolute;
    z-index: 70;
    max-width: calc(100vw - 16px);
    max-height: 60vh;
    overflow: auto;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    font-size: 13px;
    text-align: left;
  }
  .above {
    bottom: calc(100% + 4px);
  }
  .below {
    top: calc(100% + 4px);
  }
  .left {
    left: 0;
  }
  .right {
    right: 0;
  }
</style>
