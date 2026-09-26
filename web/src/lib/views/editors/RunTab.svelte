<script lang="ts">
  // Live execution viewer (process WatchEvents; falls back to
  // GetProcess every 2s if the stream fails).
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import WorldState from '../../components/WorldState.svelte';
  import StepsTimeline from '../../components/StepsTimeline.svelte';
  import HumanTaskForm from '../../components/HumanTaskForm.svelte';
  import ApprovalPanel from '../../components/ApprovalPanel.svelte';
  import FlowDecisionPanel from '../../components/FlowDecisionPanel.svelte';
  import BoardIssuesPanel from '../../components/BoardIssuesPanel.svelte';
  import FlowGraph from '../../components/FlowGraph.svelte';
  import FlowActions from '../../components/FlowActions.svelte';
  import FlowBranchInfo from '../../components/FlowBranchInfo.svelte';
  import IntentDialogue from './IntentDialogue.svelte';
  import { provideActions } from '../../shell/workbench.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import {
    engine,
    graph,
    errorMessage,
    formatDate,
    formatInt,
    shortId,
    JAEGER_URL,
    type Flow,
    type LogLine,
    type Process,
  } from '../../api';
  import { watchEvents, type StreamStatus } from '../../stream';
  import { processes, ingestProcess, ingestEvent, childrenOf, refreshProcesses } from '../../stores/live.svelte';
  import { chainOf, inChain, restartedStepNumber } from '../../flowChain';

  let { tab }: { tab: Tab } = $props();

  const id = $derived(tab.params.id ?? '');
  const process = $derived<Process | undefined>(processes.get(id));
  let error = $state('');
  let loading = $state(false);
  let stream = $state<StreamStatus>('connecting');
  let liveLogs = $state<LogLine[]>([]);

  const POLL_MS = 2000;
  /**
   * The server only sends the stream headers with the first event:
   * "connecting" is the normal state of an idle stream. We only do a
   * slow check here; polling every 2s is only used on failure.
   */
  const IDLE_POLL_MS = 10_000;
  const TERMINAL = ['completed', 'failed'];

  function set(p: Process | undefined) {
    if (p) ingestProcess(p);
  }

  /** a finished run that is not a sub-agent can be restarted from one of its steps */
  const relaunchable = $derived(
    !!process?.changeId && !process.parentId && process.status !== 'running' && process.status !== 'clarifying' && process.status !== 'superseded',
  );
  /** the process that replaced this one once its relaunched flow was adopted */
  const replacedBy = $derived(
    process?.status === 'superseded' ? [...processes.values()].find((p) => p.relaunchOf === process.id && p.status === 'completed') : undefined,
  );
  /** the runs linked to this one by relaunches (empty when it is not part of a flow chain) */
  const chain = $derived(process && inChain(process, processes) ? chainOf(process, processes) : []);
  const chainKey = $derived(chain.map((p) => `${p.id}:${p.status}`).join(','));
  let flows = $state<Flow[]>([]);
  let flowsTick = $state(0);
  $effect(() => {
    const changeId = process?.changeId;
    void flowsTick;
    if (!changeId || !chainKey) {
      flows = [];
      return;
    }
    const ctrl = new AbortController();
    graph
      .listFlows(changeId, ctrl.signal)
      .then((r) => (flows = r.flows ?? []))
      .catch(() => {});
    return () => ctrl.abort();
  });
  /** the branch the change of a run on a flow acts on (the graph branch of the flow is reviewed against it) */
  let changeBranch = $state('');
  $effect(() => {
    const changeId = process?.changeId;
    if (!changeId || !process?.flow) {
      changeBranch = '';
      return;
    }
    const ctrl = new AbortController();
    graph
      .getChange(changeId, ctrl.signal)
      .then((r) => (changeBranch = r.change?.branch ?? ''))
      .catch(() => {});
    return () => ctrl.abort();
  });
  /** the flow branch this run works on */
  const ownFlow = $derived(process?.flow ? flows.find((f) => f.id === process.flow) : undefined);
  const relaunchOfProcess = $derived(process?.relaunchOf ? processes.get(process.relaunchOf) : undefined);

  async function relaunch(step: number, reason: string, guidance = '') {
    if (!process?.id) return;
    const res = await engine.relaunchStep(process.id, step, reason, guidance);
    if (res.process?.id) {
      ingestProcess(res.process);
      openRun(res.process.id);
    }
  }

  async function refresh(signal?: AbortSignal) {
    loading = true;
    try {
      set((await engine.getProcess(id, signal)).process);
      error = '';
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  // Initial load and process stream.
  $effect(() => {
    const pid = id;
    const ctrl = new AbortController();
    void refresh(ctrl.signal);
    const stop = watchEvents({
      processId: pid,
      onEvent: (e) => {
        if (e.log && (e.log.processId ?? pid) === pid) liveLogs = [...liveLogs, e.log].slice(-500);
        // The global stream already records the event: just update the data.
        ingestEvent(e, false);
      },
      onStatus: (s) => (stream = s),
    });
    return () => {
      ctrl.abort();
      stop();
    };
  });

  const status = $derived(process?.status ?? '');

  // Fallback: periodic polling when the stream has failed.
  $effect(() => {
    if (stream === 'open' || TERMINAL.includes(status) || !id) return;
    const ctrl = new AbortController();
    const timer = setInterval(() => void refresh(ctrl.signal), stream === 'connecting' ? IDLE_POLL_MS : POLL_MS);
    return () => {
      clearInterval(timer);
      ctrl.abort();
    };
  });

  const children = $derived(childrenOf(process));
  const traceUrl = $derived(process?.traceId ? `${JAEGER_URL}/trace/${process.traceId}` : '');

  function openRun(pid: string, pin = false) {
    openTab({ kind: 'run', params: { id: pid } }, { pin });
  }

  provideActions(
    () => tab.id,
    () => [
      { id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: () => refresh() },
      {
        id: 'trace',
        label: 'Trace',
        icon: 'trace',
        disabled: !traceUrl,
        title: traceUrl ? 'Open the trace in Jaeger' : 'No trace for this process',
        run: () => window.open(traceUrl, '_blank', 'noopener'),
      },
      {
        id: 'change',
        label: 'Change',
        icon: 'diff',
        disabled: !process?.changeId,
        run: () => openTab({ kind: 'change', params: { id: process?.changeId ?? '' } }),
      },
      {
        id: 'journal',
        label: 'Execution journal',
        icon: 'list',
        disabled: !process?.changeId,
        title: process?.changeId ? 'Execution journal for this process (ticks, actions, model calls)' : 'Process without a change',
        run: () =>
          openTab({ kind: 'journal', params: { id: process?.changeId ?? '', process: process?.id ?? '', record: '' } }, { pin: true }),
      },
      {
        id: 'parent',
        label: 'Parent process',
        icon: 'runs',
        disabled: !process?.parentId,
        run: () => openRun(process?.parentId ?? ''),
      },
    ],
  );
</script>

<div class="editor-page wide">
  {#if error}<div class="alert">{error}</div>{/if}
  {#if !process}
    {#if !error}<p class="empty">Loading…</p>{/if}
  {:else}
    <div class="editor-head">
      <Icon name={process.parentId ? 'bot' : 'runs'} size={18} />
      <h2>{process.title || `Run ${shortId(process.id)}`}</h2>
      <StatusBadge status={process.status} />
      {#if process.trigger}<span class="trig" title="Launched automatically by a trigger">triggered by {process.trigger}</span>{/if}
      <span
        class="live {stream}"
        title={stream === 'retrying' || stream === 'stopped'
          ? 'Stream unavailable: polling every 2s'
          : 'Live updates (WatchEvents)'}
      >
        <span class="led" aria-hidden="true"></span>{stream === 'retrying' || stream === 'stopped' ? 'polling' : 'live'}
      </span>
      <span class="grow"></span>
      {#if traceUrl}
        <a class="trace" href={traceUrl} target="_blank" rel="noreferrer"><Icon name="external" size={13} /> Trace</a>
      {/if}
    </div>

    <div class="top">
      <section class="card">
        <dl class="meta">
          <dt>ID</dt><dd><code>{process.id}</code></dd>
          <dt>Methodology</dt><dd>{process.methodology || '—'}</dd>
          <dt>Agent</dt><dd>{#if process.agent}<code>{process.agent}</code>{:else}<span class="empty">to be determined</span>{/if}</dd>
          <dt>Planner</dt><dd>{process.planner || '—'}</dd>
          <dt>Goal</dt><dd>{#if process.goal}<code>{process.goal}</code>{:else}<span class="empty">to be determined</span>{/if}</dd>
          {#if process.trigger}
            <dt>Triggered by</dt><dd><span class="trig"><Icon name="zap" size={12} /> {process.trigger}</span></dd>
          {/if}
          {#if process.initiator?.subject}
            <dt>Initiator</dt>
            <dd>
              {process.initiator.subject}{#if process.initiator.roles?.length}
                <span class="hint"> ({process.initiator.roles.join(', ')})</span>{/if}
            </dd>
          {/if}
          {#if process.changeId}
            <dt>Change</dt>
            <dd>
              <button type="button" class="link mono" onclick={() => openTab({ kind: 'change', params: { id: process.changeId ?? '' } })}>{shortId(process.changeId)}</button>
              ·
              <button
                type="button"
                class="link"
                onclick={() =>
                  openTab({ kind: 'journal', params: { id: process.changeId ?? '', process: process.id ?? '', record: '' } }, { pin: true })}
                >execution journal</button
              >
            </dd>
          {/if}
          {#if process.baselineId}
            <dt>Baseline</dt>
            <dd><button type="button" class="link mono" onclick={() => openTab({ kind: 'baseline', params: { id: process.baselineId ?? '' } })}>{shortId(process.baselineId)}</button></dd>
          {/if}
          {#if process.parentId}
            <dt>Parent</dt>
            <dd><button type="button" class="link mono" onclick={() => openRun(process.parentId ?? '')}>{shortId(process.parentId)}</button></dd>
          {/if}
          {#if children.length}
            <dt>Sub-agents</dt>
            <dd class="kids">
              {#each children as c (c)}
                {@const cp = processes.get(c)}
                <button type="button" class="link" onclick={() => openRun(c)}>
                  {cp?.agent || shortId(c)}
                </button>
                {#if cp}<StatusBadge status={cp.status} />{/if}
              {/each}
            </dd>
          {/if}
          <dt>Created</dt><dd>{formatDate(process.createdAt)}</dd>
          {#if process.updatedAt}<dt>Updated</dt><dd>{formatDate(process.updatedAt)}</dd>{/if}
        </dl>
        {#if process.error}<pre class="perror">{process.error}</pre>{/if}
      </section>
      <section class="stats" aria-label="Totals">
        <div class="stat"><span class="v">{formatInt(process.usage?.inputTokens)}</span><span class="k">input tokens</span></div>
        <div class="stat"><span class="v">{formatInt(process.usage?.outputTokens)}</span><span class="k">output tokens</span></div>
        <div class="stat"><span class="v">{process.usage?.llmCalls ?? 0}</span><span class="k">LLM calls</span></div>
        <div class="stat"><span class="v">{process.usage?.toolCalls ?? 0}</span><span class="k">tool calls</span></div>
      </section>
    </div>

    {#if process.flow}
      <div class="card flow-banner">
        Relaunched from step {restartedStepNumber(process, processes)} of
        <button type="button" class="link mono" onclick={() => openRun(process.relaunchOf ?? '')}>{relaunchOfProcess?.title || shortId(process.relaunchOf)}</button>
        {#if relaunchOfProcess}<StatusBadge status={relaunchOfProcess.status} />{/if}
        on flow <code>{shortId(process.flow)}</code>
        {#if ownFlow}
          <StatusBadge status={ownFlow.status} />
          <FlowBranchInfo flow={ownFlow} changeBranch={changeBranch} />
          <FlowActions flow={ownFlow} changeId={process.changeId ?? ''} ondecided={() => { flowsTick++; void refreshProcesses(); }} />
        {/if}
      </div>
    {/if}
    {#if process.status === 'superseded'}
      <div class="card flow-banner">
        {#if process.flow}This relaunched flow was discarded.{:else}The outputs of this run were replaced by a relaunched flow.{/if}
        {#if replacedBy}<button type="button" class="link mono" onclick={() => openRun(replacedBy.id ?? '')}>{replacedBy.title || shortId(replacedBy.id)}</button>{/if}
      </div>
    {/if}

    {#if chain.length > 1}
      <details class="card flow-graph">
        <summary>Flow <span class="hint">{chain.length} runs · {flows.length} branch{flows.length === 1 ? '' : 'es'}</span></summary>
        <FlowGraph {processes} processId={process.id ?? ''} {flows} onopen={(pid) => openRun(pid)} />
      </details>
    {/if}

    {#if process.status === 'clarifying' || process.turns?.length}
      <IntentDialogue {process} onupdate={set} />
    {/if}

    {#if process.status === 'waiting' && process.pending}
      {#key `${process.id}:${process.pending.step}:${process.pending.action}:${process.pending.kind}`}
        {#if process.pending.kind === 'approval'}
          <ApprovalPanel {process} ondecided={set} />
        {:else if process.pending.kind === 'flow'}
          <FlowDecisionPanel {process} step={restartedStepNumber(process, processes)} ondecided={set} />
        {:else if process.pending.kind === 'board'}
          <BoardIssuesPanel {process} {flows} ondecided={set} onopen={(pid) => openRun(pid)} />
        {:else if process.pending.kind === 'relaunched'}
          {@const awaited = [...processes.values()].find((q) => q.flow === process.pending?.flowId)}
          <section class="card waiting-agent">
            <h3>Waiting for the decision on the relaunched flow</h3>
            <p>
              This run restarts when the flow relaunched to fix its blackboard is adopted or discarded
              {#if awaited?.id}: <button type="button" class="link mono" onclick={() => openRun(awaited?.id ?? '')}>{awaited.agent || shortId(awaited.id)}</button>{/if}.
            </p>
            {#if process.pending.description}<p class="hint">{process.pending.description}</p>{/if}
          </section>
        {:else if process.pending.kind === 'agent'}
          <section class="card waiting-agent">
            <h3>Waiting for a sub-agent</h3>
            <p>
              Action <code>{process.pending.action}</code> is waiting for the sub-agent
              {#if process.pending.childProcessId}
                <button type="button" class="link mono" onclick={() => openRun(process.pending?.childProcessId ?? '', true)}>
                  {processes.get(process.pending.childProcessId)?.agent || shortId(process.pending.childProcessId)}
                </button>
                {#if processes.get(process.pending.childProcessId)}
                  <StatusBadge status={processes.get(process.pending.childProcessId)?.status} />
                {/if}
              {/if} to finish. It will be replayed once that completes.
            </p>
            {#if process.pending.description}<p class="hint">{process.pending.description}</p>{/if}
          </section>
        {:else}
          <HumanTaskForm {process} onsubmitted={set} />
        {/if}
      {/key}
    {/if}

    <section class="card">
      <h3>Plan</h3>
      {#if process.plan?.length}
        <ol class="chips">
          {#each process.plan as a, i (i)}
            {@const done = (process.steps ?? []).some((s) => s.action === a && s.effectsMet)}
            <li class="chip" class:done><span class="n">{i + 1}</span>{a}</li>
          {/each}
        </ol>
      {:else}
        <p class="empty">No plan.</p>
      {/if}
      {#if process.disabled?.length}
        <h4 style="margin-top: 0.7rem">Disabled actions</h4>
        <ul class="chips">
          {#each process.disabled as a (a)}
            <li class="chip disabled">{a}</li>
          {/each}
        </ul>
      {/if}
    </section>

    <div class="two">
      <section class="card">
        <h3>World state</h3>
        <WorldState world={process.world} unknown={process.unknown} />
      </section>
      <section class="card">
        <h3>Steps <span class="hint">{process.steps?.length ?? 0}</span></h3>
        <StepsTimeline steps={process.steps} processId={process.id ?? ''} {chain} {liveLogs} onopenprocess={(c) => openRun(c)} onrelaunch={relaunchable ? relaunch : undefined} />
      </section>
    </div>
  {/if}
</div>

<style>
  .flow-graph > summary {
    cursor: pointer;
    font-weight: 600;
  }
  .flow-banner {
    border-left: 3px solid var(--warn);
  }
  .wide {
    max-width: 1400px;
  }
  .live {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.85rem;
    color: var(--muted);
  }
  .led {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--warn);
  }
  .live.open .led,
  .live.connecting .led {
    background: var(--ok);
  }
  .trig {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.85rem;
    color: var(--info);
  }
  .trace {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-weight: 600;
  }
  .top {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 0.75rem;
    align-items: start;
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(2, minmax(110px, 1fr));
    gap: 0.5rem;
    margin-bottom: 0.75rem;
  }
  .stat {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.5rem 0.7rem;
    display: grid;
  }
  .stat .v {
    font-size: 1.25rem;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }
  .stat .k {
    font-size: 0.82rem;
    color: var(--muted);
  }
  .kids {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 0.5rem;
    align-items: center;
  }
  .perror {
    margin-top: 0.6rem;
    color: var(--danger);
    background: var(--danger-soft);
    border-color: transparent;
  }
  .waiting-agent {
    border-left: 3px solid var(--warn);
  }
  .chip.done {
    background: var(--ok-soft);
    color: var(--ok);
    border-color: transparent;
  }
  .chip.disabled {
    text-decoration: line-through;
    color: var(--muted);
  }
  .two {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.6fr);
    gap: 0.75rem;
  }
  @media (max-width: 1100px) {
    .two,
    .top {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
