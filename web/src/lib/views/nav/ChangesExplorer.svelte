<script lang="ts">
  // Explorer for changes (change sets) grouped by status.
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
    { id: 'active', label: 'Active' },
    { id: 'merge_pending', label: 'Merge pending' },
    { id: 'draft', label: 'Drafts' },
    { id: 'applied', label: 'Applied' },
    { id: 'abandoned', label: 'Abandoned' },
  ];

  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived(
    changes.items.filter((c) => !q || `${c.id} ${c.title ?? ''} ${c.intent ?? ''} ${c.methodology ?? ''} ${c.namespace ?? ''}`.toLowerCase().includes(q)),
  );

  function open(c: ChangeSet, pin = false) {
    openTab({ kind: 'change', params: { id: c.id ?? '' } }, { pin });
    select({
      title: c.title || shortId(c.id),
      subtitle: 'Change',
      rows: [
        ['Id', c.id ?? ''],
        ['Status', c.status ?? ''],
        ['Intent', c.intent ?? ''],
        ['Namespace', c.namespace ?? ''],
        ['Methodology', c.methodology ?? ''],
        ['Goal', c.goal ?? ''],
        ['Starting baseline', c.baselineId ?? ''],
        ['Resulting baseline', c.resultBaselineId ?? ''],
        ['Items', String(c.items?.length ?? 0)],
        ['Created', formatDate(c.createdAt)],
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
    <input type="search" placeholder="Filter…" aria-label="Filter changes" bind:value={filter} data-no-pin />
    <button
      type="button"
      class="ghost small"
      title="Refresh"
      aria-label="Refresh"
      disabled={changes.loading}
      onclick={() => refreshChanges()}><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if changes.error}<div class="alert small">{changes.error}</div>{/if}
  <div role="tree" aria-label="Changes">
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
    <p class="empty pad">No changes listed.</p>
  {/if}
  <form class="manual" onsubmit={openManual}>
    <label for="chg-id">Open by id</label>
    <div class="row">
      <input id="chg-id" class="grow mono" type="text" bind:value={manualId} placeholder="id" data-no-pin />
      <button type="submit" class="small" disabled={!manualId.trim()}>Open</button>
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
