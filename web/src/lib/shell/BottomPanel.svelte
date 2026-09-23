<script lang="ts">
  // Console du bas : onglets des vues de la zone « bottom ».
  import Icon from './Icon.svelte';
  import { viewsIn, panelView } from './registry';
  import { layout } from './layout.svelte';

  const views = $derived(viewsIn('bottom'));
  const view = $derived(panelView(layout.bottom) ?? views[0]);
</script>

<section class="console" aria-label="Console">
  <div class="strip" role="tablist" aria-label="Console">
    {#each views as v (v.id)}
      {@const on = view?.id === v.id}
      {@const badge = v.badge?.()}
      <button type="button" role="tab" class:on aria-selected={on} onclick={() => (layout.bottom = v.id)}>
        <Icon name={v.icon} size={13} />
        {v.title}
        {#if badge}<span class="badge">{badge}</span>{/if}
      </button>
    {/each}
    <span class="grow"></span>
    <button
      type="button"
      class="close"
      title="Masquer la console (Ctrl+J)"
      aria-label="Masquer la console"
      onclick={() => (layout.bottomOpen = false)}><Icon name="x" size={14} /></button
    >
  </div>
  <div class="body" role="tabpanel">
    {#if view}
      {@const C = view.component}
      <C />
    {/if}
  </div>
</section>

<style>
  .console {
    display: flex;
    flex-direction: column;
    height: 100%;
    background: var(--surface);
    min-height: 0;
  }
  .strip {
    display: flex;
    align-items: stretch;
    height: 30px;
    flex: none;
    padding: 0 0.4rem;
    gap: 0.2rem;
    border-bottom: 1px solid var(--border);
  }
  .strip button {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    border: none;
    border-radius: 0;
    background: transparent;
    color: var(--muted);
    font-size: 0.8rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding: 0 0.55rem;
    min-height: 0;
  }
  .strip button:hover:not(:disabled) {
    color: var(--text);
    background: transparent;
  }
  .strip button.on {
    color: var(--text);
    box-shadow: inset 0 -2px 0 var(--accent);
  }
  .strip button:focus-visible {
    outline-offset: -2px;
  }
  .strip .close {
    text-transform: none;
  }
  .badge {
    background: var(--neutral-soft);
    color: var(--text);
    border-radius: 8px;
    padding: 0 5px;
    font-size: 0.75rem;
    letter-spacing: 0;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }
</style>
