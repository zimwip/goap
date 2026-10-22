<script lang="ts">
  // Vertical activity bar: the pin, then one icon per tool in the area. The pin stays at the top of the rail whether the
  // navigation is pinned or not, as on the context bar.
  import Icon from './Icon.svelte';
  import { viewsIn } from './registry';
  import { layout, nav, toggleTool } from './layout.svelte';
  import { mode, toolInMode } from './mode.svelte';

  let { side }: { side: 'left' | 'right' } = $props();

  // the left tools of the mode the person is in (ADR 0158)
  const views = $derived(side === 'left' ? viewsIn(side).filter((v) => toolInMode(v.id, mode.current)) : viewsIn(side));
  const active = $derived(side === 'left' ? layout.left : layout.right);
  const open = $derived(side === 'left' ? layout.leftOpen || nav.revealed : layout.drawer);

  // the navigation shown over the editor follows the tool hovered; closed, a click opens it
  let hoverTimer: ReturnType<typeof setTimeout> | undefined;
  function hover(id: string) {
    clearTimeout(hoverTimer);
    if (side !== 'left' || !nav.revealed || layout.left === id) return;
    hoverTimer = setTimeout(() => {
      if (nav.revealed) layout.left = id;
    }, 120);
  }

  function pin() {
    layout.leftOpen = !layout.leftOpen;
    nav.revealed = false;
  }

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
  {#if side === 'left'}
    <button
      type="button"
      class="pin"
      class:pinned={layout.leftOpen}
      aria-pressed={layout.leftOpen}
      title={layout.leftOpen ? 'Unpin: the navigation hides by its rail (Ctrl+B)' : 'Pin the navigation beside the editor (Ctrl+B)'}
      aria-label="Pin the navigation"
      onclick={pin}><Icon name="pin" size={14} /></button
    >
  {/if}
  {#each views as v (v.id)}
    {@const on = open && active === v.id}
    {@const badge = v.badge?.()}
    <button
      type="button"
      class:on
      title={v.title}
      aria-label={v.title}
      aria-pressed={on}
      onclick={() => toggleTool(side, v.id)}
      onmouseenter={() => hover(v.id)}
      onmouseleave={() => clearTimeout(hoverTimer)}
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
    padding: 0 0 6px;
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
  /* the pin: slanted while the navigation hides by its rail, upright and accented while pinned (as on the context bar) */
  .pin {
    height: 36px;
  }
  .pin :global(svg) {
    transform: rotate(45deg);
    transition: transform var(--transition);
  }
  .pin.pinned,
  .pin.pinned:hover:not(:disabled) {
    color: var(--accent);
  }
  .pin.pinned :global(svg) {
    transform: none;
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
