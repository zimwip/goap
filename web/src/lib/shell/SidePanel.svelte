<script lang="ts">
  // Panneau latéral affichant l'outil actif d'une barre d'activité.
  import { panelView } from './registry';
  import { layout } from './layout.svelte';
  import Icon from './Icon.svelte';

  let { side }: { side: 'left' | 'right' } = $props();

  const view = $derived(panelView(side === 'left' ? layout.left : layout.right));

  function close() {
    if (side === 'left') layout.leftOpen = false;
    else layout.rightOpen = false;
  }
</script>

<section class="panel" aria-label={view?.title}>
  <header>
    <h2>{view?.title ?? ''}</h2>
    <button type="button" class="ghost small icon" title="Masquer le panneau" aria-label="Masquer le panneau" onclick={close}>
      <Icon name="x" size={14} />
    </button>
  </header>
  <div class="body">
    {#if view}
      {@const C = view.component}
      <C />
    {/if}
  </div>
</section>

<style>
  .panel {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    background: var(--chrome);
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    height: 32px;
    padding: 0 0.3rem 0 0.8rem;
    flex: none;
  }
  h2 {
    flex: 1;
    margin: 0;
    font-size: 0.78rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .icon {
    padding: 0.15rem;
    color: var(--muted);
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
</style>
