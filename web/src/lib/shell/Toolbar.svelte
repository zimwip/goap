<script lang="ts">
  // Barre d'outils : actions contextuelles de l'éditeur actif, puis actions
  // globales et bascules des panneaux.
  import Icon from './Icon.svelte';
  import { tabActions, focusRequests } from './workbench.svelte';
  import { tabsState } from './tabs.svelte';
  import { layout, showTool } from './layout.svelte';
  import type { ToolbarAction } from './types';

  const actions = $derived<ToolbarAction[]>(tabActions.get(tabsState.active) ?? []);

  function newTest() {
    showTool('right', 'tester');
    focusRequests.tester += 1;
  }

  let running = $state('');
  async function run(a: ToolbarAction) {
    running = a.id;
    try {
      await a.run();
    } finally {
      running = '';
    }
  }
</script>

<div class="toolbar" role="toolbar" aria-label="Actions">
  <div class="group context">
    {#each actions as a (a.id)}
      <button
        type="button"
        class="tb"
        class:primary={a.primary}
        class:danger={a.danger}
        disabled={a.disabled || running === a.id}
        title={`${a.title ?? a.label}${a.shortcut ? ` (${a.shortcut})` : ''}`}
        onclick={() => run(a)}
      >
        {#if a.icon}<Icon name={a.icon} size={14} />{/if}
        <span class="lbl">{a.label}</span>
      </button>
    {/each}
  </div>
  <div class="group global">
    <button type="button" class="tb" title="Nouveau test d'intention" onclick={newTest}>
      <Icon name="flask" size={14} /><span class="lbl">Nouveau test d'intention</span>
    </button>
    <span class="sep" aria-hidden="true"></span>
    <button
      type="button"
      class="tb icon"
      aria-pressed={layout.leftOpen}
      title="Panneau de navigation (Ctrl+B)"
      aria-label="Panneau de navigation"
      onclick={() => (layout.leftOpen = !layout.leftOpen)}><Icon name="panelLeft" size={15} /></button
    >
    <button
      type="button"
      class="tb icon"
      aria-pressed={layout.bottomOpen}
      title="Console (Ctrl+J)"
      aria-label="Console"
      onclick={() => (layout.bottomOpen = !layout.bottomOpen)}><Icon name="panelBottom" size={15} /></button
    >
    <button
      type="button"
      class="tb icon"
      aria-pressed={layout.rightOpen}
      title="Panneau latéral droit"
      aria-label="Panneau latéral droit"
      onclick={() => (layout.rightOpen = !layout.rightOpen)}><Icon name="panelRight" size={15} /></button
    >
  </div>
</div>

<style>
  .toolbar {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    height: 34px;
    padding: 0 0.5rem;
    flex: none;
    background: var(--chrome);
    border-bottom: 1px solid var(--border);
    overflow: hidden;
  }
  .group {
    display: flex;
    align-items: center;
    gap: 2px;
    min-width: 0;
  }
  .context {
    flex: 1;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tb {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    flex: none;
    min-height: 24px;
    padding: 0.1rem 0.5rem;
    border-color: transparent;
    background: transparent;
    font-weight: 500;
    font-size: 0.92rem;
    white-space: nowrap;
  }
  .tb:hover:not(:disabled) {
    background: var(--hover);
  }
  .tb.primary {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .tb.primary:hover:not(:disabled) {
    background: var(--accent);
    filter: brightness(1.08);
  }
  .tb.icon {
    padding: 0.1rem 0.35rem;
    color: var(--muted);
  }
  .tb.icon[aria-pressed='true'] {
    color: var(--text);
  }
  .sep {
    width: 1px;
    height: 18px;
    background: var(--border);
    margin: 0 0.3rem;
  }
  @media (max-width: 1180px) {
    .global .lbl {
      display: none;
    }
  }
</style>
