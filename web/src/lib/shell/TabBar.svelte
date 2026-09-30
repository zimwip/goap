<script lang="ts">
  // Tab bar for the editor area.
  import Icon from './Icon.svelte';
  import { editorView } from './registry';
  import { tabsState, activate, closeTab, togglePin, pinTab, moveTab, isDirty } from './tabs.svelte';

  let dragFrom = -1;
  let list: HTMLDivElement;
  let overflow = $state(false);
  let canLeft = $state(false);
  let canRight = $state(false);
  let menu = $state(false);

  function measure() {
    if (!list) return;
    overflow = list.scrollWidth > list.clientWidth + 1;
    canLeft = list.scrollLeft > 0;
    canRight = list.scrollLeft + list.clientWidth < list.scrollWidth - 1;
  }

  function scrollBy(dir: number) {
    list.scrollBy({ left: dir * Math.max(120, list.clientWidth * 0.6), behavior: 'smooth' });
  }

  function pick(id: string) {
    menu = false;
    activate(id);
  }

  $effect(() => {
    if (!list) return;
    const ro = new ResizeObserver(measure);
    ro.observe(list);
    return () => ro.disconnect();
  });

  // Re-measure when tabs come and go.
  $effect(() => {
    void tabsState.tabs.length;
    queueMicrotask(measure);
  });

  function aux(e: MouseEvent, id: string) {
    if (e.button === 1) {
      e.preventDefault();
      closeTab(id);
    }
  }

  function keydown(e: KeyboardEvent, i: number) {
    const n = tabsState.tabs.length;
    let j = -1;
    if (e.key === 'ArrowRight') j = (i + 1) % n;
    else if (e.key === 'ArrowLeft') j = (i - 1 + n) % n;
    else if (e.key === 'Home') j = 0;
    else if (e.key === 'End') j = n - 1;
    else if (e.key === 'Delete') {
      closeTab(tabsState.tabs[i].id);
      e.preventDefault();
      return;
    } else return;
    e.preventDefault();
    activate(tabsState.tabs[j].id);
    list.querySelectorAll<HTMLElement>('[role=tab]')[j]?.focus();
  }

  // Keep the active tab visible when it changes.
  $effect(() => {
    const id = tabsState.active;
    if (!id || !list) return;
    list.querySelector<HTMLElement>(`[data-tab="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  });

  function outside(e: MouseEvent) {
    if (menu && !(e.target as HTMLElement).closest('.more')) menu = false;
  }

  function wheel(e: WheelEvent) {
    if (Math.abs(e.deltaY) > Math.abs(e.deltaX)) {
      list.scrollLeft += e.deltaY;
    }
  }
</script>

<svelte:window onclick={outside} />
<div class="bar">
  {#if overflow}
    <button type="button" class="nav" title="Scroll tabs left" aria-label="Scroll tabs left" disabled={!canLeft} onclick={() => scrollBy(-1)}
      ><Icon name="chevronLeft" size={14} /></button
    >
  {/if}
<div class="tabs" role="tablist" aria-label="Open tabs" bind:this={list} onwheel={wheel} onscroll={measure}>
  {#each tabsState.tabs as tab, i (tab.id)}
    {@const v = editorView(tab.kind)}
    {@const on = tab.id === tabsState.active}
    {@const dirty = isDirty(tab)}
    {@const title = v?.tabTitle(tab) ?? tab.id}
    <div
      class="tab"
      class:on
      class:preview={!tab.pinned}
      class:dirty
      data-tab={tab.id}
      role="tab"
      tabindex={on ? 0 : -1}
      aria-selected={on}
      title={`${v?.tooltip?.(tab) ?? title}${tab.pinned ? '' : ' (preview — double-click to pin)'}`}
      draggable="true"
      onclick={() => activate(tab.id)}
      ondblclick={() => pinTab(tab.id)}
      onauxclick={(e) => aux(e, tab.id)}
      onmousedown={(e) => e.button === 1 && e.preventDefault()}
      onkeydown={(e) => keydown(e, i)}
      ondragstart={(e) => {
        dragFrom = i;
        e.dataTransfer?.setData('text/plain', tab.id);
      }}
      ondragover={(e) => e.preventDefault()}
      ondrop={(e) => {
        e.preventDefault();
        moveTab(dragFrom, i);
        dragFrom = -1;
      }}
    >
      {#if v}<span class="ticon"><Icon name={v.tabIcon?.(tab) ?? v.icon} size={14} /></span>{/if}
      <span class="label">{title}</span>
      {#if tab.pinned}
        <button
          type="button"
          class="tbtn pin"
          tabindex="-1"
          title="Unpin"
          aria-label={`Unpin ${title}`}
          onclick={(e) => {
            e.stopPropagation();
            togglePin(tab.id);
          }}><Icon name="pin" size={12} /></button
        >
      {/if}
      <button
        type="button"
        class="tbtn close"
        tabindex="-1"
        title={dirty ? 'Unsaved changes — close' : 'Close (Ctrl+W)'}
        aria-label={`Close ${title}`}
        onclick={(e) => {
          e.stopPropagation();
          closeTab(tab.id);
        }}
      >
        <span class="dot" aria-hidden="true">●</span>
        <span class="x"><Icon name="x" size={13} /></span>
      </button>
    </div>
  {/each}
</div>
  {#if overflow}
    <button type="button" class="nav" title="Scroll tabs right" aria-label="Scroll tabs right" disabled={!canRight} onclick={() => scrollBy(1)}
      ><Icon name="chevronRight" size={14} /></button
    >
    <div class="more">
      <button type="button" class="nav" title="All open tabs" aria-label="All open tabs" aria-expanded={menu} onclick={() => (menu = !menu)}
        ><Icon name="more" size={14} /></button
      >
      {#if menu}
        <ul class="menu" role="menu">
          {#each tabsState.tabs as tab (tab.id)}
            {@const v = editorView(tab.kind)}
            {@const t = v?.tabTitle(tab) ?? tab.id}
            <li role="none">
              <button type="button" role="menuitem" class:on={tab.id === tabsState.active} title={v?.tooltip?.(tab) ?? t} onclick={() => pick(tab.id)}>
                {#if v}<Icon name={v.tabIcon?.(tab) ?? v.icon} size={13} />{/if}
                <span>{t}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</div>

<style>
  .bar {
    display: flex;
    flex: none;
    align-items: stretch;
    background: var(--chrome);
    border-bottom: 1px solid var(--border);
    position: relative;
  }
  .nav {
    display: grid;
    place-items: center;
    width: 24px;
    min-height: 0;
    padding: 0;
    border: none;
    border-radius: 0;
    background: transparent;
    color: var(--muted);
    flex: none;
  }
  .nav:hover:not(:disabled) {
    background: var(--hover);
    color: var(--text);
  }
  .nav:disabled {
    opacity: 0.35;
  }
  .more {
    position: relative;
    display: flex;
    border-left: 1px solid var(--border);
  }
  .menu {
    position: absolute;
    top: 100%;
    right: 0;
    z-index: 50;
    margin: 0;
    padding: 0.2rem;
    list-style: none;
    min-width: 200px;
    max-width: 380px;
    max-height: 60vh;
    overflow-y: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: 0 6px 24px rgb(0 0 0 / 0.35);
  }
  .menu button {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    width: 100%;
    min-height: 0;
    padding: 0.3rem 0.5rem;
    border: none;
    background: transparent;
    color: var(--text);
    text-align: left;
  }
  .menu button:hover {
    background: var(--hover);
  }
  .menu button.on {
    color: var(--accent);
  }
  .menu span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tabs {
    flex: 1;
    min-width: 0;
    display: flex;
    height: 34px;
    flex: none;
    overflow-x: auto;
    overflow-y: hidden;
    scrollbar-width: none;
  }
  .tab {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    flex: 0 1 200px;
    min-width: 96px;
    padding: 0 0.3rem 0 0.7rem;
    border-right: 1px solid var(--border);
    color: var(--muted);
    cursor: pointer;
    user-select: none;
    white-space: nowrap;
    position: relative;
  }
  .tab:hover {
    background: var(--hover);
  }
  .tab.on {
    background: var(--bg);
    color: var(--text);
    margin-bottom: -1px;
    box-shadow: inset 0 2px 0 var(--accent);
  }
  .tab:focus-visible {
    outline-offset: -2px;
  }
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .preview .label {
    font-style: italic;
  }
  .ticon {
    color: var(--accent);
    opacity: 0.85;
  }
  .tbtn {
    display: grid;
    place-items: center;
    width: 20px;
    height: 20px;
    min-height: 0;
    padding: 0;
    border: none;
    background: transparent;
    color: inherit;
    border-radius: var(--radius-sm);
  }
  .tbtn:hover:not(:disabled) {
    background: var(--hover);
  }
  .pin {
    opacity: 0.6;
  }
  .close .dot {
    display: none;
    font-size: 11px;
  }
  .close .x {
    visibility: hidden;
  }
  .tab.on .close .x,
  .tab:hover .close .x {
    visibility: visible;
  }
  .dirty .close .dot {
    display: block;
  }
  .dirty .close .x {
    display: none;
  }
  .dirty .close:hover .dot {
    display: none;
  }
  .dirty .close:hover .x {
    display: block;
    visibility: visible;
  }
</style>
