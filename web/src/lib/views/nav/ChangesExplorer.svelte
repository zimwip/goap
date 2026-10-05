<script lang="ts">
  // Explorer for changes (the blackboard of every modification) grouped by status.
  // Each change nests the executions (agent processes) that work on it.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import NewChangeForm from '../../components/NewChangeForm.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { changes, refreshChanges } from '../../stores/catalog.svelte';
  import { live, processes, refreshProcesses } from '../../stores/live.svelte';
  import { showTool } from '../../shell/layout.svelte';
  import { select as select, focusRequests } from '../../shell/workbench.svelte';
  import { openTab, tabsState } from '../../shell/tabs.svelte';
  import { formatDate, formatInt, shortId, int, type Change, type Process } from '../../api';

  let filter = $state('');
  let manualId = $state('');
  let creating = $state(false);
  // Sub-changes nested under their parent change (tree) or listed on their own (flat).
  let tree = $state(true);

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
      if (!p.changeId || nested(p)) continue;
      m.set(p.changeId, [...(m.get(p.changeId) ?? []), p]);
    }
    return m;
  });
  const subs = $derived.by(() => {
    const m = new Map<string, Process[]>();
    for (const p of processes.values()) if (p.parentId && processes.has(p.parentId)) m.set(p.parentId, [...(m.get(p.parentId) ?? []), p]);
    return m;
  });
  // Runs fired by a trigger on an event of another run: children of the run that raised it.
  const caused = $derived.by(() => {
    const m = new Map<string, Process[]>();
    for (const p of processes.values()) if (!p.parentId && p.cause && processes.has(p.cause)) m.set(p.cause, [...(m.get(p.cause) ?? []), p]);
    return m;
  });
  const orphans = $derived([...processes.values()].filter((p) => !p.changeId && !nested(p)));

  // A run shown under another one: sub-agent of its parent, or fired by an event of its cause.
  function nested(p: Process): boolean {
    return !!((p.parentId && processes.has(p.parentId)) || (p.cause && processes.has(p.cause)));
  }

  // Runs of one level, a trigger being the parent of the runs it launched (parallel or re-runs): the group
  // stands where its newest run was.
  type Entry = { trigger: string; runs: Process[] } | { run: Process };
  function layout(list: Process[]): Entry[] {
    const out: Entry[] = [];
    const groups = new Map<string, { trigger: string; runs: Process[] }>();
    for (const p of list) {
      if (!p.trigger) {
        out.push({ run: p });
        continue;
      }
      let g = groups.get(p.trigger);
      if (!g) {
        g = { trigger: p.trigger, runs: [] };
        groups.set(p.trigger, g);
        out.push(g);
      }
      g.runs.push(p);
    }
    return out;
  }

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
    { id: 'committed', label: 'Committed' },
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

  const runsOf = (c: Change) => byChange.get(c.id ?? '') ?? [];

  const shownIds = $derived(new Set(shown.map((c) => c.id)));
  const subChanges = $derived.by(() => {
    const m = new Map<string, Change[]>();
    for (const c of shown) if (c.parentId && shownIds.has(c.parentId)) m.set(c.parentId, [...(m.get(c.parentId) ?? []), c]);
    return m;
  });
  const isRoot = (c: Change) => !tree || !(c.parentId && shownIds.has(c.parentId));
  const childrenOf = (c: Change) => (tree ? (subChanges.get(c.id ?? '') ?? []) : []);

  function open(c: Change, pin = false) {
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

{#snippet runs(list: Process[], depth: number, scope: string)}
  {#each layout(list) as e ('run' in e ? e.run.id : `t:${scope}:${e.trigger}`)}
    {#if 'run' in e}
      {@render run(e.run, depth)}
    {:else}
      {@const k = `t:${scope}:${e.trigger}`}
      <TreeRow
        {depth}
        icon="zap"
        label={e.trigger}
        detail={String(e.runs.length)}
        title={`triggered by ${e.trigger}`}
        expanded={isOpen(k, true)}
        ontoggle={() => toggle(k, true)}
      />
      {#if isOpen(k, true)}
        {#each e.runs as p (p.id)}{@render run(p, depth + 1)}{/each}
      {/if}
    {/if}
  {/each}
{/snippet}

{#snippet run(p: Process, depth: number)}
  {@const kids = subs.get(p.id ?? '') ?? []}
  {@const fired = caused.get(p.id ?? '') ?? []}
  {@const k = `p:${p.id}`}
  {@const tokens = int(p.usage?.inputTokens) + int(p.usage?.outputTokens)}
  <TreeRow
    {depth}
    icon={p.parentId ? 'bot' : 'runs'}
    label={plabel(p)}
    detail={shortId(p.id)}
    expanded={kids.length || fired.length ? isOpen(k, true) : undefined}
    active={tabsState.active === `run:${p.id}`}
    title={`${plabel(p)} — ${p.status}${tokens ? ` — ${formatInt(tokens)} tokens` : ''}${p.trigger ? `\ntriggered by ${p.trigger}` : ''}\n${formatDate(p.createdAt)}`}
    onselect={() => openRun(p)}
    onopen={() => openRun(p, true)}
    ontoggle={() => toggle(k, true)}
  >
    {#snippet trail()}<StatusBadge status={p.status} />{/snippet}
  </TreeRow>
  {#if (kids.length || fired.length) && isOpen(k, true)}
    {#each kids as c (c.id)}{@render run(c, depth + 1)}{/each}
    {@render runs(fired, depth + 1, k)}
  {/if}
{/snippet}

{#snippet change(c: Change, depth: number)}
  {@const subsOf = childrenOf(c)}
  {@const k = `ch:${c.id}`}
  {@const hasKids = runsOf(c).length > 0 || subsOf.length > 0}
  <TreeRow
    {depth}
    icon="diff"
    label={c.title || shortId(c.id)}
    detail={formatDate(c.createdAt)}
    title={c.intent || c.title || c.id}
    active={tabsState.active === `change:${c.id}`}
    expanded={hasKids ? isOpen(k, true) : undefined}
    ontoggle={() => toggle(k, true)}
    onselect={() => open(c)}
    onopen={() => open(c, true)}
  >
    {#snippet trail()}<StatusBadge status={c.status} />{/snippet}
  </TreeRow>
  {#if hasKids && isOpen(k, true)}
    {@render runs(runsOf(c), depth + 1, k)}
    {#each subsOf as sc (sc.id)}{@render change(sc, depth + 1)}{/each}
  {/if}
{/snippet}

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filter…" aria-label="Filter changes" bind:value={filter} data-no-pin />
    <button type="button" class="ghost small" title="New change (by hand, no methodology needed)" aria-label="New change" aria-pressed={creating} onclick={() => (creating = !creating)}
      ><Icon name="plus" size={14} /></button
    >
    <button type="button" class="ghost small" title="New intent test" aria-label="New intent test" onclick={newTest}
      ><Icon name="flask" size={14} /></button
    >
    <button
      type="button"
      class="ghost small"
      title={tree ? 'Sub-changes under their parent (click for a flat list)' : 'Flat list of changes (click to nest sub-changes)'}
      aria-label="Nest sub-changes under their parent"
      aria-pressed={tree}
      onclick={() => (tree = !tree)}><Icon name={tree ? 'trace' : 'list'} size={14} /></button
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
  {#if creating}
    <NewChangeForm
      oncreated={(cid) => ((creating = false), openTab({ kind: 'change', params: { id: cid } }, { pin: true }))}
      oncancel={() => (creating = false)}
    />
  {/if}
  {#if changes.error}<div class="alert small">{changes.error}</div>{/if}
  <div role="tree" aria-label="Changes">
    {#each STATUSES as s (s.id)}
      {@const list = shown.filter((c) => c.status === s.id)}
      {@const k = `c:${s.id}`}
      {#if list.length}
        <TreeRow icon="folder" label={s.label} detail={String(list.length)} expanded={isOpen(k, s.id !== 'abandoned')} ontoggle={() => toggle(k, s.id !== 'abandoned')} />
        {#if isOpen(k, s.id !== 'abandoned')}
          {#each list.filter(isRoot) as c (c.id)}{@render change(c, 1)}{/each}
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
        {@render runs(orphans, 1, 'orphans')}
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
