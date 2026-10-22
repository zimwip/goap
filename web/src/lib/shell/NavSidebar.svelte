<script lang="ts">
  // Navigation sidebar: the navigation of the tool the activity bar selected (ADR 0158), headed by its name. The bar
  // is the tool selector; the sidebar only hides (Ctrl+B, or a second click on the tool's icon).
  import Icon from './Icon.svelte';
  import { panelView } from './registry';
  import { layout } from './layout.svelte';

  const view = $derived(panelView(layout.left));
  const badge = $derived(view?.badge?.());
</script>

<section class="sidebar" aria-label="Navigation">
  <div class="head">
    {#if view}<Icon name={view.icon} size={14} /><span class="label">{view.title}</span>{/if}
    {#if badge}<span class="count">{badge}</span>{/if}
    <button type="button" class="ghost small icon" title="Hide the navigation (Ctrl+B)" aria-label="Hide the navigation" onclick={() => (layout.leftOpen = false)}>
      <Icon name="chevronLeft" size={14} />
    </button>
  </div>

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
  .head {
    flex: none;
    display: flex;
    align-items: center;
    gap: 7px;
    min-height: 34px;
    padding: 0 var(--space-1) 0 10px;
    border-bottom: 1px solid var(--border);
    font-weight: 600;
    font-size: 12.5px;
  }
  .head > :global(svg) {
    color: var(--accent);
    flex: none;
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
    font-weight: 400;
    line-height: 16px;
    text-align: center;
  }
  .icon {
    padding: 0.15rem;
    color: var(--text-3);
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
</style>
