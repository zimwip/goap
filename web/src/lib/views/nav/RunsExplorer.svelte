<script lang="ts">
  // Explorateur des exécutions : processus regroupés par statut, sous-agents
  // imbriqués sous leur processus parent.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { live, processes, refreshProcesses } from '../../stores/live.svelte';
  import { openTab, tabsState } from '../../shell/tabs.svelte';
  import { select, focusRequests } from '../../shell/workbench.svelte';
  import { showTool } from '../../shell/layout.svelte';
  import { formatDate, formatInt, shortId, int, type Process } from '../../api';
  import type { IconName } from '../../shell/Icon.svelte';

  let filter = $state('');

  $effect(() => {
    if (!live.processesLoaded) void refreshProcesses();
  });

  const GROUPS: { id: string; label: string; statuses: string[]; icon: IconName }[] = [
    { id: 'active', label: 'En cours', statuses: ['running', 'clarifying'], icon: 'play' },
    { id: 'waiting', label: 'En attente', statuses: ['waiting'], icon: 'user' },
    { id: 'done', label: 'Terminées', statuses: ['completed'], icon: 'check' },
    { id: 'failed', label: 'Échecs et blocages', statuses: ['failed', 'stuck'], icon: 'alert' },
  ];

  function label(p: Process): string {
    return p.title || p.agent || p.goal || shortId(p.id);
  }

  const q = $derived(filter.trim().toLowerCase());
  const all = $derived(
    [...processes.values()].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? '')),
  );
  const matches = (p: Process) =>
    !q || `${p.id} ${p.title ?? ''} ${p.agent ?? ''} ${p.goal ?? ''} ${p.methodology ?? ''}`.toLowerCase().includes(q);

  /** Racines : processus sans parent connu. */
  const roots = $derived(all.filter((p) => !p.parentId || !processes.has(p.parentId)));
  const byParent = $derived.by(() => {
    const m = new Map<string, Process[]>();
    for (const p of all) if (p.parentId && processes.has(p.parentId)) m.set(p.parentId, [...(m.get(p.parentId) ?? []), p]);
    return m;
  });

  function subtreeMatches(p: Process): boolean {
    return matches(p) || (byParent.get(p.id ?? '') ?? []).some(subtreeMatches);
  }

  function open(p: Process, pin = false) {
    openTab({ kind: 'run', params: { id: p.id ?? '' } }, { pin });
    select({
      title: label(p),
      subtitle: 'Exécution',
      rows: [
        ['Identifiant', p.id ?? ''],
        ['Statut', p.status ?? ''],
        ['Méthodologie', p.methodology ?? ''],
        ['Agent', p.agent ?? ''],
        ['Planificateur', p.planner ?? ''],
        ['Objectif', p.goal ?? ''],
        ['Déclenché par', p.trigger ?? ''],
        ['Initiateur', p.initiator?.subject ?? ''],
        ['Tokens (entrée / sortie)', `${formatInt(p.usage?.inputTokens)} / ${formatInt(p.usage?.outputTokens)}`],
        ['Appels LLM / outils', `${p.usage?.llmCalls ?? 0} / ${p.usage?.toolCalls ?? 0}`],
        ['Créé', formatDate(p.createdAt)],
        ['Mis à jour', formatDate(p.updatedAt)],
      ],
    });
  }

  function newTest() {
    showTool('right', 'tester');
    focusRequests.tester += 1;
  }
</script>

{#snippet node(p: Process, depth: number)}
  {@const kids = byParent.get(p.id ?? '') ?? []}
  {@const k = `p:${p.id}`}
  {@const tokens = int(p.usage?.inputTokens) + int(p.usage?.outputTokens)}
  <TreeRow
    {depth}
    icon={p.parentId ? 'bot' : p.trigger ? 'zap' : 'runs'}
    label={label(p)}
    detail={[p.agent && p.agent !== label(p) ? p.agent : '', shortId(p.id)].filter(Boolean).join(' · ')}
    expanded={kids.length ? isOpen(k, true) : undefined}
    active={tabsState.active === `run:${p.id}`}
    title={`${label(p)} — ${p.status}${tokens ? ` — ${formatInt(tokens)} tokens` : ''}${p.trigger ? `\ndéclenché par ${p.trigger}` : ''}\n${formatDate(p.createdAt)}`}
    onselect={() => open(p)}
    onopen={() => open(p, true)}
    ontoggle={() => toggle(k, true)}
  >
    {#snippet trail()}
      <StatusBadge status={p.status} />
    {/snippet}
  </TreeRow>
  {#if kids.length && isOpen(k, true)}
    {#each kids as c (c.id)}
      {#if subtreeMatches(c)}{@render node(c, depth + 1)}{/if}
    {/each}
  {/if}
{/snippet}

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filtrer…" aria-label="Filtrer les exécutions" bind:value={filter} data-no-pin />
    <button type="button" class="ghost small" title="Nouveau test d'intention" aria-label="Nouveau test d'intention" onclick={newTest}
      ><Icon name="flask" size={14} /></button
    >
    <button
      type="button"
      class="ghost small"
      title="Actualiser"
      aria-label="Actualiser"
      disabled={live.processesLoading}
      onclick={() => refreshProcesses()}><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if live.processesError}<div class="alert small">{live.processesError}</div>{/if}
  {#if live.processesLoaded && !all.length && !live.processesError}
    <p class="empty pad">Aucune exécution. Lancez un test d'intention depuis l'outil « Tester ».</p>
  {/if}
  <div role="tree" aria-label="Exécutions">
    {#each GROUPS as g (g.id)}
      {@const list = roots.filter((p) => g.statuses.includes(p.status ?? '') && subtreeMatches(p))}
      {@const gk = `g:${g.id}`}
      {#if list.length}
        <TreeRow
          icon={g.icon}
          label={g.label}
          detail={String(list.length)}
          expanded={isOpen(gk, g.id !== 'done')}
          ontoggle={() => toggle(gk, g.id !== 'done')}
        />
        {#if isOpen(gk, g.id !== 'done')}
          {#each list as p (p.id)}{@render node(p, 1)}{/each}
        {/if}
      {/if}
    {/each}
    {#if roots.some((p) => !GROUPS.some((g) => g.statuses.includes(p.status ?? '')))}
      <TreeRow icon="help" label="Autres" expanded={isOpen('g:other')} ontoggle={() => toggle('g:other')} />
      {#if isOpen('g:other')}
        {#each roots.filter((p) => !GROUPS.some((g) => g.statuses.includes(p.status ?? ''))) as p (p.id)}{@render node(p, 1)}{/each}
      {/if}
    {/if}
  </div>
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
</style>
