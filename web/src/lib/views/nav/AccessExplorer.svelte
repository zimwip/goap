<script lang="ts">
  // Outil « Accès » : aperçu des politiques ABAC ; l'édition se fait dans l'onglet « Politiques ».
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { iam, errorMessage, type Policy } from '../../api';
  import { openTab } from '../../shell/tabs.svelte';
  import { select } from '../../shell/workbench.svelte';

  let policies = $state<Policy[]>([]);
  let error = $state('');
  let loading = $state(false);

  async function load() {
    loading = true;
    try {
      policies = (await iam.listPolicies()).policies ?? [];
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void load();
  });

  function open(p?: Policy, pin = false) {
    openTab({ kind: 'policies', params: {} }, { pin });
    if (p)
      select({
        title: `${p.effect} ${p.resource}/${p.action}`,
        subtitle: 'Politique ABAC',
        rows: [
          ['Règle', p.rule ?? ''],
          ['Ressource', p.resource ?? ''],
          ['Action', p.action ?? ''],
          ['Effet', p.effect ?? ''],
        ],
      });
  }
</script>

<div class="explorer">
  <div class="tools">
    <button type="button" class="small" onclick={() => open(undefined, true)}>
      <Icon name="shield" size={13} /> Éditer les politiques
    </button>
    <span class="grow"></span>
    <button type="button" class="ghost small" title="Actualiser" aria-label="Actualiser" disabled={loading} onclick={load}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if error}<div class="alert small">{error}</div>{/if}
  <div role="tree" aria-label="Politiques">
    {#each policies as p, i (i)}
      <TreeRow
        icon="shield"
        label={`${p.resource}/${p.action}`}
        detail={p.rule}
        onselect={() => open(p)}
        onopen={() => open(p, true)}
      >
        {#snippet trail()}<StatusBadge status={p.effect} />{/snippet}
      </TreeRow>
    {:else}
      {#if !loading && !error}<p class="empty pad">Aucune politique.</p>{/if}
    {/each}
  </div>
</div>

<style>
  .tools {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
  }
  .tools button {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
