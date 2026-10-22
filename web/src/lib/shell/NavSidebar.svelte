<script lang="ts">
  // Navigation sidebar: the tool shown, then its content. The tools of the left area (named and counted) are listed
  // only while the person switches: the list folds back to the selection once a tool is picked, so navigation stays
  // at hand without taking the height. Not pinned (Ctrl+B), the activity bar's icons stand in for the sidebar, which shows over the editor from them.
  import Icon from './Icon.svelte';
  import { panelView, viewsIn } from './registry';
  import { layout, nav, showTool } from './layout.svelte';
  import { mode, toolInMode } from './mode.svelte';
  import type { PanelView } from './types';

  // the tools of the mode the person is in (ADR 0158)
  const tools = $derived(viewsIn('left').filter((v) => toolInMode(v.id, mode.current)));
  const view = $derived(panelView(layout.left));
  const viewBadge = $derived(view?.badge?.());
  // the tools that call for attention while the list is folded
  const waiting = $derived(tools.filter((v) => v.id !== view?.id && v.badge?.()).length);
  let listing = $state(false);

  function pin() {
    layout.leftOpen = !layout.leftOpen;
    nav.revealed = false;
  }

  function pick(v: PanelView) {
    showTool('left', v.id);
    listing = false;
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && listing) {
      listing = false;
      e.preventDefault();
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const items = [...(e.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('.ui-nav-item')];
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    items[(i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus();
    e.preventDefault();
  }
</script>

<section class="sidebar" aria-label="Navigation">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <nav class="nav" aria-label="Workbench" onkeydown={keydown}>
    <div class="current">
      <button type="button" class="pick" aria-expanded={listing} aria-controls="workbench-tools" title="Switch the tool" onclick={() => (listing = !listing)}>
        <span class="ui-section-head lead">Workbench</span>
        {#if view}<Icon name={view.icon} size={14} /><span class="label">{view.title}</span>{/if}
        {#if viewBadge}<span class="count">{viewBadge}</span>{/if}
        {#if !listing && waiting}<span class="dot" title="{waiting} other tool{waiting > 1 ? 's' : ''} call for attention"></span>{/if}
        <span class="chev" class:open={listing}><Icon name="chevronDown" size={12} /></span>
      </button>
      <button
        type="button"
        class="ghost small icon"
        class:on={layout.leftOpen}
        aria-pressed={layout.leftOpen}
        title={layout.leftOpen ? 'Unpin: the navigation hides by its rail (Ctrl+B)' : 'Pin the navigation beside the editor (Ctrl+B)'}
        aria-label="Pin the navigation"
        onclick={pin}
      >
        <Icon name="pin" size={14} />
      </button>
    </div>
    {#if listing}
      <ul id="workbench-tools">
        {#each tools as v (v.id)}
          {@const on = layout.left === v.id}
          {@const badge = v.badge?.()}
          <li>
            <button type="button" class="ui-nav-item" class:active={on} aria-current={on ? 'true' : undefined} aria-label={badge ? `${v.title}, ${badge}` : v.title} onclick={() => pick(v)}>
              <Icon name={v.icon} size={14} />
              <span class="label">{v.title}</span>
              {#if badge}<span class="count" aria-hidden="true">{badge}</span>{/if}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </nav>

  <div class="body">
    {#if view}
      {@const C = view.component}
      <C />
    {/if}
  </div>
</section>

<style>
  .sidebar {
    container: sidebar / inline-size;
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    background: var(--bg-panel);
  }
  .nav {
    flex: none;
    max-height: 60%;
    overflow: auto;
    padding: var(--space-1) var(--space-1) var(--space-1) var(--space-1);
    border-bottom: 1px solid var(--border);
  }
  .current {
    display: flex;
    align-items: center;
    gap: 2px;
  }
  .pick {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 7px;
    min-height: 30px;
    padding: 0 6px 0 0;
    border: none;
    background: none;
    text-align: left;
    font-weight: 600;
    font-size: 12.5px;
    color: var(--text);
  }
  .pick:hover:not(:disabled) {
    background: var(--bg-raised);
    border-color: transparent;
  }
  .pick :global(svg) {
    color: var(--accent);
    flex: none;
  }
  .lead {
    padding: 0 4px 0 8px;
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--warning);
  }
  .chev {
    display: inline-flex;
    color: var(--text-3);
    transition: transform var(--transition);
  }
  .chev :global(svg) {
    color: var(--text-3);
  }
  .chev.open {
    transform: rotate(180deg);
  }
  ul {
    list-style: none;
    margin: var(--space-1) 0;
    padding: 0;
    display: grid;
    gap: 1px;
  }
  /* a wide sidebar lays the tools out in two columns, leaving the height to the tool shown */
  @container sidebar (min-width: 250px) {
    ul {
      grid-template-columns: 1fr 1fr;
      column-gap: 2px;
    }
  }
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .count {
    min-width: 18px;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--bg-inset);
    border: 1px solid var(--border);
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    line-height: 16px;
    text-align: center;
  }
  .ui-nav-item.active .count {
    border-color: var(--accent);
    color: var(--accent);
  }
  .icon {
    padding: 0.15rem;
    color: var(--text-3);
  }
  /* the pin: slanted while the navigation hides by its rail, upright and accented while pinned */
  .icon :global(svg) {
    transform: rotate(45deg);
    transition: transform var(--transition);
  }
  .icon.on {
    color: var(--accent);
    background: var(--accent-soft, var(--hover));
  }
  .icon.on :global(svg) {
    transform: none;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
</style>
