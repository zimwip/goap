<script lang="ts">
  // Zone d'édition : onglets et éditeur de l'onglet actif.
  import TabBar from './TabBar.svelte';
  import { editorView } from './registry';
  import { tabsState, activeTab, pinTab, isDirty } from './tabs.svelte';
  import type { Component } from 'svelte';

  let { welcome }: { welcome: Component } = $props();

  const tab = $derived(activeTab());
  const view = $derived(tab ? editorView(tab.kind) : undefined);

  // Un aperçu devient épinglé dès qu'il est modifié.
  $effect(() => {
    if (tab && !tab.pinned && isDirty(tab)) pinTab(tab.id);
  });

  function edited(e: Event) {
    const t = e.target as HTMLElement | null;
    if (!tab || tab.pinned || !t) return;
    // Filtres de listes / recherches : pas une modification du contenu.
    if (t.closest('[data-no-pin]')) return;
    pinTab(tab.id);
  }
</script>

<div class="area">
  {#if tabsState.tabs.length}
    <TabBar />
  {/if}
  <div class="content" role="tabpanel" aria-label={view?.tabTitle(tab!) ?? 'Accueil'} oninput={edited}>
    {#if tab && view}
      {#key tab.id}
        {@const C = view.component}
        <C {tab} />
      {/key}
    {:else if tab}
      <p class="empty pad">Type d'onglet inconnu : {tab.kind}</p>
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
