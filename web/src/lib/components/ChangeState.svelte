<script lang="ts">
  // The current state of a change (ADR 0112): each process of its methodology once, where it stands now, whatever the
  // number of runs it took (a reactive process, such as the review of the description, may run at every edit). The
  // steps are judged against the change as it is, then what the live runs do is laid over them; a step that cannot
  // start says which conditions it still needs. The runs themselves stay one click away.
  // What the steps are is the compiled view of the version the change follows (registry, ADR 0126 step 4); where they
  // stand is the engine's binding, laid over it here. A process reads as a list or as its flow (the methodology's own
  // flow view, simplified by default, with the state of each step).
  import { engine, registry, type ChangeStateResponse, type LevelCheck, type MethodologyView, type ProcessGraph, type ProcessState } from '../api';
  import { processes } from '../stores/live.svelte';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { blockers, composeProcess, needDetail, needsOf, ordered, runsText, statesByPath } from '../changeState';
  import ProcessProgress from './ProcessProgress.svelte';
  import ProcessFlowView from './ProcessFlowView.svelte';

  let { changeId, onopen }: { changeId: string; onopen?: (id: string) => void } = $props();

  let cs = $state<ChangeStateResponse | undefined>();
  let error = $state('');
  // the compiled view of the version the change follows: fixed for a version, read once
  let view = $state<MethodologyView | undefined>();
  let viewOf = '';
  $effect(() => {
    const name = cs?.methodology ?? '';
    const ref = `${name}@${cs?.version ?? ''}`;
    if (!name || ref === viewOf) return;
    viewOf = ref;
    registry
      .storedMethodologyView(name, cs?.version ?? '')
      .then((r) => (view = r.view))
      .catch((e: unknown) => (error = e instanceof Error ? e.message : String(e)));
  });
  const composed = $derived(
    (cs?.processes ?? []).map((p) => {
      const c = composeProcess(view, p);
      return { state: p, activity: c.activity, steps: c.steps };
    }),
  );
  // the flow of a process, read when the reader asks for it
  let shown = $state<Record<string, 'list' | 'flow'>>({});
  let flows = $state<Record<string, { graph?: ProcessGraph; levels?: LevelCheck[]; conditions?: { name: string; expr: string }[]; error?: string }>>({});
  function showFlow(process: string) {
    show(process, shown[process] === 'flow' ? 'list' : 'flow');
  }
  // every process at once: the display the reader picks in the header
  const allShown = $derived((cs?.processes ?? []).length > 0 && (cs?.processes ?? []).every((p) => shown[p.process ?? ''] === 'flow') ? 'flow' : 'list');
  function showAll(as: 'list' | 'flow') {
    for (const p of cs?.processes ?? []) show(p.process ?? '', as);
  }
  function show(process: string, as: 'list' | 'flow') {
    shown = { ...shown, [process]: as };
    if (as !== 'flow' || flows[process] || !cs?.methodology) return;
    const name = cs.methodology;
    const version = cs.version ?? '';
    Promise.all([registry.storedProcessGraph(name, version, process), registry.storedLevels(name, version, process)])
      .then(([g, l]) => {
        flows = { ...flows, [process]: { graph: g.graph, levels: l.levels ?? [], conditions: Object.entries(l.conditions ?? {}).map(([n, expr]) => ({ name: n, expr })) } };
      })
      .catch((e: unknown) => (flows = { ...flows, [process]: { error: e instanceof Error ? e.message : String(e) } }));
  }
  // the runs of the change move (live stream) or the change itself does: read the state again
  const version = $derived(
    [...processes.values()]
      .filter((p) => p.changeId === changeId)
      .map((p) => `${p.id}:${p.status}:${p.updatedAt}`)
      .join('|'),
  );
  $effect(() => {
    void version;
    void stamp(keyOf.change(changeId));
    const ctl = new AbortController();
    engine
      .getChangeState(changeId, ctl.signal)
      .then((r) => {
        cs = r;
        error = '';
      })
      .catch((e: unknown) => {
        if (!ctl.signal.aborted) error = e instanceof Error ? e.message : String(e);
      });
    return () => ctl.abort();
  });

  const LABEL: Record<string, string> = {
    done: 'done',
    skipped: 'not needed',
    active: 'in progress',
    waiting: 'waiting',
    ready: 'ready',
    todo: 'to do',
    blocked: 'blocked',
  };
  const progressOf = (p: ProcessState, steps: ReturnType<typeof composeProcess>['steps']) => ({ process: p.process, steps, done: p.done, total: p.total, status: p.status, error: p.error });
  const pct = (p: ProcessState) => (p.total ? Math.round(((p.done ?? 0) * 100) / p.total) : 0);
</script>

{#if error}
  <p class="hint">Current state unavailable: {error}</p>
{:else if cs?.processes?.length}
  <section class="card state" aria-label="Current state">
    <h3>
      Current state
      {#if cs.goalReached}<span class="chip done">goal reached</span>{/if}
      <span class="as" role="group" aria-label="Show the processes">
        <button type="button" class="small" aria-pressed={allShown === 'list'} title="Each process as a list of its steps" onclick={() => showAll('list')}>List</button>
        <button type="button" class="small" aria-pressed={allShown === 'flow'} title="Each process as its flow, with where each step stands (Simplified or Detailed in the flow)" onclick={() => showAll('flow')}>Flow</button>
      </span>
    </h3>
    {#each ordered(cs.processes) as p (p.process)}
      {@const c = composed.find((x) => x.state.process === p.process)}
      {@const steps = c?.steps ?? []}
      {@const stuck = blockers(steps)}
      {@const flow = flows[p.process ?? '']}
      <details class="proc" open={p.state !== 'done' && p.state !== 'skipped'}>
        <summary>
          <span class="chip {p.state}">{LABEL[p.state ?? ''] ?? p.state}</span>
          <strong>{p.process}</strong>
          {#if c?.activity?.reactive}<span class="hint" title="Runs again on the events of the change, in the same run">reacts to the change</span>{/if}
          <span class="hint">{p.done ?? 0}/{p.total ?? 0} steps · {runsText(p)}</span>
          {#if p.current}<button type="button" class="link" onclick={(e) => { e.preventDefault(); onopen?.(p.current ?? ''); }}>open run</button>{/if}
          <button type="button" class="link" aria-pressed={shown[p.process ?? ''] === 'flow'} onclick={(e) => { e.preventDefault(); showFlow(p.process ?? ''); }}>{shown[p.process ?? ''] === 'flow' ? 'as a list' : 'as a flow'}</button>
        </summary>
        <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={pct(p)}><span style="width: {pct(p)}%"></span></div>
        {#if p.error && p.state !== 'done'}<p class="hint">{p.error}</p>{/if}
        {#if stuck.length && p.state !== 'done'}
          <div class="needs">
            <span class="hint">Waiting for:</span>
            <ul>
              {#each stuck as s (s.path)}
                <li><strong>{s.name}</strong> needs {needsOf(s).map(needDetail).join('; ')}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if shown[p.process ?? ''] === 'flow'}
          {#if flow?.error}<p class="hint">Flow unavailable: {flow.error}</p>
          {:else if flow?.graph}
            <div class="flow"><ProcessFlowView graph={flow.graph} levels={flow.levels} conditions={flow.conditions} states={statesByPath(steps)} mode="simple" /></div>
          {:else}<p class="hint">Drawing the flow…</p>{/if}
        {:else if view}
          <ProcessProgress given={progressOf(p, steps)} {onopen} bare />
        {:else}<p class="hint">Reading the steps…</p>{/if}
      </details>
    {/each}
  </section>
{/if}

<style>
  h3 {
    display: flex;
    gap: 8px;
    align-items: center;
    margin: 0 0 6px;
  }
  .as {
    margin-left: auto;
    display: inline-flex;
    gap: 2px;
    font-weight: normal;
  }
  .as button[aria-pressed='true'] {
    border-color: var(--accent);
    color: var(--accent);
  }
  .proc {
    margin: 6px 0;
  }
  .flow {
    margin: 4px 0;
  }
  .proc summary {
    display: flex;
    gap: 8px;
    align-items: baseline;
    cursor: pointer;
  }
  .bar {
    height: 4px;
    background: var(--border);
    border-radius: 3px;
    overflow: hidden;
    margin: 4px 0;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--ok);
  }
  .needs {
    margin: 4px 0 6px;
    padding: 4px 8px;
    border-left: 3px solid var(--warn);
    background: var(--warn-soft);
    font-size: 0.92em;
  }
  .needs ul {
    margin: 2px 0 0;
    padding-left: 1.2em;
  }
  .chip {
    display: inline-block;
    min-width: 6em;
    text-align: center;
    font-size: 0.8em;
    border-radius: 8px;
    padding: 0 6px;
    border: 1px solid var(--border);
    color: var(--muted);
  }
  .chip.done {
    color: var(--ok);
    border-color: var(--ok);
  }
  .chip.active {
    color: var(--info);
    border-color: var(--info);
  }
  .chip.waiting {
    color: var(--warn);
    border-color: var(--warn);
    background: var(--warn-soft);
  }
  .chip.ready {
    color: var(--accent);
    border-color: var(--accent);
  }
  .chip.blocked {
    color: var(--danger);
    border-color: var(--danger);
    background: var(--danger-soft);
  }
</style>
