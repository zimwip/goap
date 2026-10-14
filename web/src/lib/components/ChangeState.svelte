<script lang="ts">
  // The current state of a change (ADR 0112): each process of its methodology once, where it stands now, whatever the
  // number of runs it took (a reactive process, such as the review of the description, may run at every edit). The
  // steps are judged against the change as it is, then what the live runs do is laid over them; a step that cannot
  // start says which conditions it still needs. The runs themselves stay one click away.
  import { engine, type ChangeStateResponse, type ProcessState } from '../api';
  import { processes } from '../stores/live.svelte';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { blockers, needDetail, needsOf, ordered, runsText } from '../changeState';
  import ProcessProgress from './ProcessProgress.svelte';

  let { changeId, onopen }: { changeId: string; onopen?: (id: string) => void } = $props();

  let cs = $state<ChangeStateResponse | undefined>();
  let error = $state('');
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
  const progressOf = (p: ProcessState) => ({ process: p.process, steps: p.steps, done: p.done, total: p.total, status: p.status, error: p.error });
  const pct = (p: ProcessState) => (p.total ? Math.round(((p.done ?? 0) * 100) / p.total) : 0);
</script>

{#if error}
  <p class="hint">Current state unavailable: {error}</p>
{:else if cs?.processes?.length}
  <section class="card state" aria-label="Current state">
    <h3>
      Current state
      {#if cs.goalReached}<span class="chip done">goal reached</span>{/if}
    </h3>
    {#each ordered(cs.processes) as p (p.process)}
      {@const stuck = blockers(p.steps)}
      <details class="proc" open={p.state !== 'done' && p.state !== 'skipped'}>
        <summary>
          <span class="chip {p.state}">{LABEL[p.state ?? ''] ?? p.state}</span>
          <strong>{p.process}</strong>
          {#if p.reactive}<span class="hint" title="Runs again on the events of the change, in the same run">reacts to the change</span>{/if}
          <span class="hint">{p.done ?? 0}/{p.total ?? 0} steps · {runsText(p)}</span>
          {#if p.current}<button type="button" class="link" onclick={(e) => { e.preventDefault(); onopen?.(p.current ?? ''); }}>open run</button>{/if}
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
        <ProcessProgress given={progressOf(p)} {onopen} bare />
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
  .proc {
    margin: 6px 0;
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
