<script lang="ts">
  // Explorateur des changements (change sets) regroupés par statut.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { changes, refreshChanges } from '../../stores/catalog.svelte';
  import { openTab, tabsState } from '../../shell/tabs.svelte';
  import { select } from '../../shell/workbench.svelte';
  import { formatDate, shortId, type ChangeSet } from '../../api';

  let filter = $state('');
  let manualId = $state('');

  $effect(() => {
    if (!changes.loaded) void refreshChanges();
  });

  const STATUSES = [
    { id: 'active', label: 'Actifs' },
    { id: 'draft', label: 'Brouillons' },
    { id: 'applied', label: 'Appliqués' },
    { id: 'abandoned', label: 'Abandonnés' },
  ];

  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived(
    changes.items.filter((c) => !q || `${c.id} ${c.title ?? ''} ${c.intent ?? ''} ${c.methodology ?? ''}`.toLowerCase().includes(q)),
  );

  function open(c: ChangeSet, pin = false) {
    openTab({ kind: 'change', params: { id: c.id ?? '' } }, { pin });
    select({
      title: c.title || shortId(c.id),
      subtitle: 'Changement',
      rows: [
        ['Identifiant', c.id ?? ''],
        ['Statut', c.status ?? ''],
        ['Intention', c.intent ?? ''],
        ['Méthodologie', c.methodology ?? ''],
        ['Objectif', c.goal ?? ''],
        ['Référentiel de départ', c.baselineId ?? ''],
        ['Référentiel résultant', c.resultBaselineId ?? ''],
        ['Items', String(c.items?.length ?? 0)],
        ['Créé', formatDate(c.createdAt)],
      ],
    });
  }

  function openManual(e: SubmitEvent) {
    e.preventDefault();
    if (manualId.trim()) openTab({ kind: 'change', params: { id: manualId.trim() } }, { pin: true });
    manualId = '';
  }
</script>

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filtrer…" aria-label="Filtrer les changements" bind:value={filter} data-no-pin />
    <button
      type="button"
      class="ghost small"
      title="Actualiser"
      aria-label="Actualiser"
      disabled={changes.loading}
      onclick={() => refreshChanges()}><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if changes.error}<div class="alert small">{changes.error}</div>{/if}
  <div role="tree" aria-label="Changements">
    {#each STATUSES as s (s.id)}
      {@const list = shown.filter((c) => c.status === s.id)}
      {@const k = `c:${s.id}`}
      {#if list.length}
        <TreeRow icon="folder" label={s.label} detail={String(list.length)} expanded={isOpen(k, s.id !== 'abandoned')} ontoggle={() => toggle(k, s.id !== 'abandoned')} />
        {#if isOpen(k, s.id !== 'abandoned')}
          {#each list as c (c.id)}
            <TreeRow
              depth={1}
              icon="diff"
              label={c.title || shortId(c.id)}
              detail={formatDate(c.createdAt)}
              title={c.intent || c.title || c.id}
              active={tabsState.active === `change:${c.id}`}
              onselect={() => open(c)}
              onopen={() => open(c, true)}
            >
              {#snippet trail()}<StatusBadge status={c.status} />{/snippet}
            </TreeRow>
          {/each}
        {/if}
      {/if}
    {/each}
    {#if shown.some((c) => !STATUSES.some((s) => s.id === c.status))}
      {#each shown.filter((c) => !STATUSES.some((s) => s.id === c.status)) as c (c.id)}
        <TreeRow icon="diff" label={c.title || shortId(c.id)} onselect={() => open(c)} onopen={() => open(c, true)} />
      {/each}
    {/if}
  </div>
  {#if changes.loaded && !changes.items.length}
    <p class="empty pad">Aucun changement listé.</p>
  {/if}
  <form class="manual" onsubmit={openManual}>
    <label for="chg-id">Ouvrir par identifiant</label>
    <div class="row">
      <input id="chg-id" class="grow mono" type="text" bind:value={manualId} placeholder="identifiant" data-no-pin />
      <button type="submit" class="small" disabled={!manualId.trim()}>Ouvrir</button>
    </div>
  </form>
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
    position: sticky;
    top: 0;
    background: var(--chrome);
    z-index: 1;
  }
  .tools input {
    min-height: 24px;
    height: 24px;
    margin-right: 0.2rem;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
  .manual {
    padding: 0.8rem 0.6rem 0;
  }
  .manual .row {
    flex-wrap: nowrap;
  }
</style>
