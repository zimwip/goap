<script lang="ts">
  // Vertical activity bar: one icon per tool in the area. On the left it selects the tool: its navigation beside the
  // editors, or the view it builds in the editor space (ADR 0158).
  import Icon from './Icon.svelte';
  import { viewsIn } from './registry';
  import { layout, toggleTool } from './layout.svelte';
  import { mode, toolInMode } from './mode.svelte';
  import type { PanelView } from './types';

  let { side }: { side: 'left' | 'right' } = $props();

  // the left tools of the mode the person is in (ADR 0158)
  const views = $derived(side === 'left' ? viewsIn(side).filter((v) => toolInMode(v.id, mode.current)) : viewsIn(side));
  /** whether a tool is the one shown */
  const shown = (v: PanelView): boolean =>
    side === 'right' ? layout.drawer && layout.right === v.id : v.fill ? layout.full === v.id : !layout.full && layout.leftOpen && layout.left === v.id;

  function keydown(e: KeyboardEvent) {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const buttons = [...(e.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button')];
    const i = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next = buttons[(i + (e.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length];
    next?.focus();
    e.preventDefault();
  }
</script>

<div class="bar {side}" role="toolbar" aria-orientation="vertical" tabindex="-1" aria-label={side === 'left' ? 'Navigation tools' : 'Side tools'} onkeydown={keydown}>
  {#each views as v (v.id)}
    {@const on = shown(v)}
    {@const badge = v.badge?.()}
    <button
      type="button"
      class:on
      title={v.title}
      aria-label={v.title}
      aria-pressed={on}
      onclick={() => toggleTool(side, v.id)}
    >
      <Icon name={v.icon} size={20} />
      {#if badge}<span class="badge">{badge}</span>{/if}
    </button>
  {/each}
</div>

<style>
  .bar {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    width: 42px;
    flex: none;
    padding: 6px 0;
    background: var(--bg-panel-translucent);
  }
  .left {
    border-right: 1px solid var(--border);
  }
  .right {
    border-left: 1px solid var(--border);
  }
  button {
    position: relative;
    width: 42px;
    height: 40px;
    display: grid;
    place-items: center;
    border: none;
    border-radius: 0;
    background: transparent;
    color: var(--text-3);
    padding: 0;
    transition: color var(--transition);
  }
  button:hover:not(:disabled) {
    color: var(--text);
    background: transparent;
  }
  button.on {
    color: var(--accent);
  }
  .left button.on {
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .right button.on {
    box-shadow: inset -2px 0 0 var(--accent);
  }
  button:focus-visible {
    outline-offset: -2px;
  }
  .badge {
    position: absolute;
    right: 4px;
    bottom: 5px;
    min-width: 15px;
    height: 15px;
    padding: 0 3px;
    border-radius: 8px;
    background: var(--accent);
    color: var(--accent-text);
    font-size: 9.5px;
    font-weight: 700;
    line-height: 15px;
    text-align: center;
  }
</style>
