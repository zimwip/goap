<script lang="ts">
  import { engine, errorMessage, type Process } from '../api';

  let { process, step, ondecided }: { process: Process; step?: number; ondecided: (p: Process) => void } = $props();

  let comment = $state('');
  let busy = $state(false);
  let error = $state('');

  async function decide(adopt: boolean) {
    if (!process.id) return;
    busy = true;
    error = '';
    try {
      const res = await engine.decideFlow(process.id, adopt, comment.trim());
      if (res.process) ondecided(res.process);
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<section class="card flow-decision">
  <h3>Relaunched flow ready</h3>
  <p>
    The run restarted from step {step ?? (process.fromStep ?? 0) + 1} reached its goal. Adopt it to replace
    the outputs of the previous run (they become superseded), or discard it to keep the previous
    outputs.
  </p>
  {#if process.pending?.description}<p class="hint">{process.pending.description}</p>{/if}
  <label class="field">
    <span>Comment (optional)</span>
    <textarea rows="2" bind:value={comment}></textarea>
  </label>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="row">
    <button class="primary" disabled={busy} onclick={() => decide(true)}>Adopt this flow</button>
    <button disabled={busy} onclick={() => decide(false)}>Discard</button>
  </div>
</section>

<style>
  .flow-decision {
    border-left: 3px solid var(--warn);
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
