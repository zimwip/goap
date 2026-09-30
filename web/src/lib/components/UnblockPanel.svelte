<script lang="ts">
  // A run waiting for conditions established outside it, or stuck, is never left without a way out (ADR 0036 §3):
  // whoever answers for it (its initiator, the accountable role of its step, a member of its organisation) declares
  // conditions established (a waiver recorded on the change, with its reason), retries what it gave up on, or abandons it.
  import { engine, errorMessage, type Process } from '../api';

  let { process, ondecided }: { process: Process; ondecided: (p: Process) => void } = $props();

  const stuck = $derived(process.status === 'stuck');
  const conditions = $derived(process.pending?.conditions ?? []);
  const disabled = $derived(process.disabled ?? []);
  let chosen = $state<string[]>([]);
  let reason = $state('');
  let busy = $state(false);
  let error = $state('');

  $effect(() => {
    chosen = [...conditions];
  });

  function toggle(c: string) {
    chosen = chosen.includes(c) ? chosen.filter((x) => x !== c) : [...chosen, c];
  }

  async function decide(decision: 'waive' | 'retry' | 'abandon') {
    if (!process.id) return;
    busy = true;
    error = '';
    try {
      const res = await engine.unblockProcess(process.id, decision, decision === 'waive' ? chosen : [], reason.trim());
      if (res.process) ondecided(res.process);
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<section class="card unblock" class:stuck>
  {#if stuck}
    <h3>Stuck — a person must decide</h3>
    <p>No plan reaches the goal{process.error ? ` (${process.error})` : ''}, whatever other processes establish.</p>
  {:else}
    <h3>Waiting for conditions</h3>
    <p>
      No action of this agent establishes what it needs: another process, a person or the state of the change will. The run is
      tried again whenever the change moves; you may also decide now.
    </p>
  {/if}
  {#if conditions.length}
    <div class="conds">
      <span class="hint">{stuck ? 'Would unblock it:' : 'Waiting for:'}</span>
      {#each conditions as c (c)}
        <label class="cond"><input type="checkbox" checked={chosen.includes(c)} onchange={() => toggle(c)} /> <code>{c}</code></label>
      {/each}
    </div>
  {/if}
  {#if disabled.length}
    <p class="hint">Given up on: {#each disabled as a, i (a)}{i ? ', ' : ''}<code>{a}</code>{/each}</p>
  {/if}
  <label class="field">
    <span>Reason (recorded on the change; required to waive or abandon)</span>
    <textarea rows="2" bind:value={reason}></textarea>
  </label>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="row">
    {#if conditions.length}
      <button class="primary" disabled={busy || !chosen.length || !reason.trim()} onclick={() => decide('waive')}>Declare established and continue</button>
    {/if}
    {#if stuck}
      <button disabled={busy} onclick={() => decide('retry')}>Retry</button>
    {/if}
    <button class="danger" disabled={busy || !reason.trim()} onclick={() => decide('abandon')}>Abandon the run</button>
  </div>
</section>

<style>
  .unblock {
    border-left: 3px solid var(--warn);
  }
  .unblock.stuck {
    border-left-color: var(--err, var(--warn));
  }
  .conds {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 12px;
    align-items: center;
    margin: 6px 0;
  }
  .cond {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 8px 0;
  }
  .hint {
    color: var(--muted);
  }
</style>
