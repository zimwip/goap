<script lang="ts">
  // Editor area: tabs and the active tab's editor.
  import TabBar from './TabBar.svelte';
  import { editorView } from './registry';
  import { tabsState, activeTab, pinTab, isDirty } from './tabs.svelte';
  import type { Component } from 'svelte';

  let { welcome }: { welcome: Component } = $props();

  const tab = $derived(activeTab());
  const view = $derived(tab ? editorView(tab.kind) : undefined);

  // A preview becomes pinned as soon as it's modified.
  $effect(() => {
    if (tab && !tab.pinned && isDirty(tab)) pinTab(tab.id);
  });

  function edited(e: Event) {
    const t = e.target as HTMLElement | null;
    if (!tab || tab.pinned || !t) return;
    // List filters / searches: not a content modification.
    if (t.closest('[data-no-pin]')) return;
    pinTab(tab.id);
  }
</script>

<div class="area">
  {#if tabsState.tabs.length}
    <TabBar />
  {/if}
  <div class="content" role="tabpanel" aria-label={view?.tabTitle(tab!) ?? 'Home'} oninput={edited}>
    {#if tab && view}
      {#key tab.id}
        {@const C = view.component}
        <C {tab} />
      {/key}
    {:else if tab}
      <p class="empty pad">Unknown tab type: {tab.kind}</p>
    {:else}
      {@const W = welcome}
      <W />
    {/if}
  </div>
</div>

<style>
  .area {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    background: var(--bg);
  }
  .content {
    flex: 1;
    min-height: 0;
    overflow: auto;
    position: relative;
  }
  .pad {
    padding: 1rem;
  }
</style>
