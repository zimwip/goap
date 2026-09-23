<script lang="ts">
  // Outil « Propriétés » : détails de l'objet sélectionné (explorateurs) ou,
  // à défaut, de l'onglet actif.
  import { selection } from '../../shell/workbench.svelte';
  import { activeTab } from '../../shell/tabs.svelte';
  import { editorView } from '../../shell/registry';

  const fromTab = $derived.by(() => {
    const t = activeTab();
    return t ? editorView(t.kind)?.properties?.(t) : undefined;
  });
  const props = $derived(selection.current ?? fromTab);
</script>

<div class="props">
  {#if props}
    <h3>{props.title}</h3>
    {#if props.subtitle}<p class="hint">{props.subtitle}</p>{/if}
    <dl>
      {#each props.rows.filter((r) => r[1] !== '') as [k, v] (k)}
        <dt>{k}</dt>
        <dd>{v}</dd>
      {/each}
    </dl>
    {#if selection.current && fromTab}
      <button type="button" class="small" onclick={() => (selection.current = undefined)}>Afficher l'onglet actif</button>
    {/if}
  {:else}
    <p class="empty">Sélectionnez un objet dans un explorateur.</p>
  {/if}
</div>

<style>
  .props {
    padding: 0 0.8rem 1rem;
  }
  h3 {
    margin: 0 0 0.1rem;
    overflow-wrap: anywhere;
  }
  dl {
    margin: 0.5rem 0;
    display: grid;
    gap: 0.1rem;
  }
  dt {
    font-size: 0.8rem;
    font-weight: 600;
    color: var(--muted);
    margin-top: 0.35rem;
  }
  dd {
    margin: 0;
    font-family: var(--mono);
    font-size: 0.92em;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }
</style>
