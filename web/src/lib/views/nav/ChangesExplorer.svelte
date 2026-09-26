<script lang="ts">
  // Explorer for changes (the blackboard of every modification) grouped by status.
  // Each change nests the executions (agent processes) that work on it.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { changes, refreshChanges } from '../../stores/catalog.svelte';
  import { live, processes, refreshProcesses } from '../../stores/live.svelte';
  import { showTool } from '../../shell/layout.svelte';
  import { select as select, focusRequests } from '../../shell/workbench.svelte';
  import { openTab, tabsState } from '../../shell/tabs.svelte';
  import { formatDate, formatInt, shortId, int, type ChangeSet, type Process } from '../../api';

  let filter = $state('');
  let manualId = $state('');

  $effect(() => {
    if (!changes.loaded) void refreshChanges();
  });
  $effect(() => {
    if (!live.processesLoaded) void refreshProcesses();
  });

  const byChange = $derived.by(() => {
    const m = new Map<string, Process[]>();
    const all = [...processes.values()].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
    for (const p of all) {
      if (!p.changeId || (p.parentId && processes.has(p.parentId))) continue;
      m.set(p.changeId, [...(m.get(p.changeId) ?? []), p]);
    }
    return m;
  });
  const subs = $derived.by(() => {
    const m = new Map<string, Process[]>();
    for (const p of processes.values()) if (p.parentId && processes.has(p.parentId)) m.set(p.parentId, [...(m.get(p.parentId) ?? []), p]);
    return m;
  });
  const orphans = $derived(
    [...processes.values()].filter((p) => !p.changeId && !(p.parentId && processes.has(p.parentId))),
  );

  const plabel = (p: Process) => p.title || p.agent || p.goal || shortId(p.id);

  function openRun(p: Process, pin = false) {
    openTab({ kind: 'run', params: { id: p.id ?? '' } }, { pin });
    select({
      title: plabel(p),
      subtitle: 'Execution',
      rows: [
        ['Id', p.id ?? ''],
        ['Change', p.changeId ?? ''],
        ['Status', p.status ?? ''],
        ['Methodology', p.methodology ?? ''],
        ['Agent', p.agent ?? ''],
        ['Planner', p.planner ?? ''],
        ['Goal', p.goal ?? ''],
        ['Triggered by', p.trigger ?? ''],
        ['Initiator', p.initiator?.subject ?? ''],
        ['Tokens (input / output)', `${formatInt(p.usage?.inputTokens)} / ${formatInt(p.usage?.outputTokens)}`],
        ['LLM / tool calls', `${p.usage?.llmCalls ?? 0} / ${p.usage?.toolCalls ?? 0}`],
        ['Created', formatDate(p.createdAt)],
      ],
    });
  }

  function newTest() {
    showTool('right', 'tester');
    focusRequests.tester += 1;
  }

  const STATUSES = [
    { id: 'active', label: 'Active' },
    { id: 'merge_pending', label: 'Merge pending' },
    { id: 'draft', label: 'Drafts' },
    { id: 'applied', label: 'Applied' },
    { id: 'abandoned', label: 'Abandoned' },
  ];

  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived(
    changes.items.filter(
      (c) =>
        !q ||
        `${c.id} ${c.title ?? ''} ${c.intent ?? ''} ${c.methodology ?? ''} ${c.namespace ?? ''} ${(byChange.get(c.id ?? '') ?? []).map((p) => `${p.agent ?? ''} ${p.goal ?? ''}`).join(' ')}`
          .toLowerCase()
          .includes(q),
    ),
  );

  const runsOf = (c: ChangeSet) => byChange.get(c.id ?? '') ?? [];

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

{#snippet run(p: Process, depth: number)}
  {@const kids = subs.get(p.id ?? '') ?? []}
  {@const k = `p:${p.id}`}
  {@const tokens = int(p.usage?.inputTokens) + int(p.usage?.outputTokens)}
  <TreeRow
    {depth}
    icon={p.parentId ? 'bot' : p.trigger ? 'zap' : 'runs'}
    label={plabel(p)}
    detail={shortId(p.id)}
    expanded={kids.length ? isOpen(k, true) : undefined}
    active={tabsState.active === `run:${p.id}`}
    title={`${plabel(p)} — ${p.status}${tokens ? ` — ${formatInt(tokens)} tokens` : ''}${p.trigger ? `\ntriggered by ${p.trigger}` : ''}\n${formatDate(p.createdAt)}`}
    onselect={() => openRun(p)}
    onopen={() => openRun(p, true)}
    ontoggle={() => toggle(k, true)}
  >
    {#snippet trail()}<StatusBadge status={p.status} />{/snippet}
  </TreeRow>
  {#if kids.length && isOpen(k, true)}
    {#each kids as c (c.id)}{@render run(c, depth + 1)}{/each}
  {/if}
{/snippet}

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filter…" aria-label="Filter changes" bind:value={filter} data-no-pin />
    <button type="button" class="ghost small" title="New intent test" aria-label="New intent test" onclick={newTest}
      ><Icon name="flask" size={14} /></button
    >
    <button
      type="button"
      class="ghost small"
      title="Refresh"
      aria-label="Refresh"
      disabled={changes.loading || live.processesLoading}
      onclick={() => {
        void refreshChanges();
        void refreshProcesses();
      }}><Icon name="refresh" size={14} /></button
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
              expanded={runsOf(c).length ? isOpen(`ch:${c.id}`, true) : undefined}
              ontoggle={() => toggle(`ch:${c.id}`, true)}
              onselect={() => open(c)}
              onopen={() => open(c, true)}
            >
              {#snippet trail()}<StatusBadge status={c.status} />{/snippet}
            </TreeRow>
            {#if runsOf(c).length && isOpen(`ch:${c.id}`, true)}
              {#each runsOf(c) as p (p.id)}{@render run(p, 2)}{/each}
            {/if}
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
  {#if orphans.length}
    <div role="tree" aria-label="Executions without a change">
      <TreeRow icon="help" label="No change" detail={String(orphans.length)} expanded={isOpen('g:orphans')} ontoggle={() => toggle('g:orphans')} />
      {#if isOpen('g:orphans')}
        {#each orphans as p (p.id)}{@render run(p, 1)}{/each}
      {/if}
    </div>
  {/if}
  {#if changes.loaded && !changes.items.length && !orphans.length}
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
