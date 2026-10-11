<script lang="ts">
  // A run whose apply stopped because rebasing the change onto what it builds on left conflicts (engine task "rebase",
  // ADR 0101): lists them, offers the change's own sync pane to settle them ("Keep mine", or edit the fields in the
  // change tab), and continues the run once they are settled and the impacts reviewed again.
  import { engine, graph, errorMessage, type Change, type Process } from '../api';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { conflictLines, rebasedOnto } from '../rebase';
  import SubChangeSync from './SubChangeSync.svelte';

  let { process, ondecided }: { process: Process; ondecided: (p: Process) => void } = $props();

  const task = $derived(process.pending?.rebase);
  const changeId = $derived(process.changeId ?? '');
  let change = $state<Change | undefined>();
  let busy = $state(false);
  let error = $state('');

  async function load() {
    if (!changeId) return;
    try {
      change = (await graph.getChange(changeId)).change;
    } catch (e) {
      error = errorMessage(e);
    }
  }

  $effect(() => {
    void changeId;
    stamp(keyOf.change(changeId)); // the change moved: read it again
    void load();
  });

  async function carryOn() {
    if (!process.id) return;
    busy = true;
    error = '';
    try {
      const res = await engine.resolveRebase(process.id);
      if (res.process) ondecided(res.process);
    } catch (e) {
      error = errorMessage(e); // conflicts or reviews remain: the engine says which
    } finally {
      busy = false;
    }
  }
</script>

{#if task}
  <section class="card rebase-task">
    <h3>The change conflicts with what it was rebased onto — {task.impacts?.length ?? 0} impact{task.impacts?.length === 1 ? '' : 's'}</h3>
    {#if process.pending?.description}<p>{process.pending.description}</p>{/if}
    <p class="hint">Rebased onto {rebasedOnto(task)}. Settle each conflict (edit the field, or keep your values), review the impacts again, then continue.</p>
    <ul>
      {#each conflictLines(task.impacts ?? []) as line (line)}<li>{line}</li>{/each}
    </ul>
    {#if change}<SubChangeSync {change} onchange={load} />{/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <div class="actions">
      <button type="button" onclick={() => openTab({ kind: 'change', params: { id: changeId } })}>Open the change</button>
      <button type="button" class="primary" onclick={carryOn} disabled={busy}>Settled, continue</button>
    </div>
  </section>
{/if}

<style>
  .actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.75rem;
  }
  .error {
    color: var(--danger, #b00020);
  }
</style>
