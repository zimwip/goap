<script lang="ts">
  // Barre d'onglets de la zone d'édition.
  import Icon from './Icon.svelte';
  import { editorView } from './registry';
  import { tabsState, activate, closeTab, togglePin, pinTab, moveTab, isDirty } from './tabs.svelte';

  let dragFrom = -1;
  let list: HTMLDivElement;

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

  // Onglet actif visible quand il change.
  $effect(() => {
    const id = tabsState.active;
    if (!id || !list) return;
    list.querySelector<HTMLElement>(`[data-tab="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  });

  function wheel(e: WheelEvent) {
    if (Math.abs(e.deltaY) > Math.abs(e.deltaX)) {
      list.scrollLeft += e.deltaY;
    }
  }
</script>

<div class="tabs" role="tablist" aria-label="Onglets ouverts" bind:this={list} onwheel={wheel}>
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
      title={`${v?.tooltip?.(tab) ?? title}${tab.pinned ? '' : ' (aperçu — double-cliquez pour épingler)'}`}
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
          title="Désépingler"
          aria-label={`Désépingler ${title}`}
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
        title={dirty ? 'Modifications non enregistrées — fermer' : 'Fermer (Ctrl+W)'}
        aria-label={`Fermer ${title}`}
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

<style>
  .tabs {
    display: flex;
    height: 34px;
    flex: none;
    overflow-x: auto;
    overflow-y: hidden;
    background: var(--chrome);
    border-bottom: 1px solid var(--border);
    scrollbar-width: thin;
  }
  .tab {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    flex: none;
    max-width: 240px;
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
