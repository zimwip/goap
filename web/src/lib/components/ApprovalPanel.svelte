<script lang="ts">
  import { engine, errorMessage, type Process } from '../api';

  let { process, ondecided }: { process: Process; ondecided: (p: Process) => void } = $props();

  const task = $derived(process.pending);
  let comment = $state('');
  let busy = $state(false);
  let error = $state('');

  async function decide(approve: boolean) {
    if (!process.id) return;
    busy = true;
    error = '';
    try {
      const res = await engine.approveAction(process.id, approve, comment.trim());
      if (res.process) ondecided(res.process);
    } catch (err) {
      // PermissionDenied: the current user does not have the required permission
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<section class="card approval">
  <h3>Approval required</h3>
  <p>
    Action <code>{task?.action}</code> requires permission <code>{task?.permission}</code>, which
    the process initiator does not have. An authorized person must approve it.
  </p>
  {#if task?.description}<p class="hint">{task.description}</p>{/if}
  <label class="field">
    <span>Comment (optional)</span>
    <textarea rows="2" bind:value={comment}></textarea>
  </label>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="row">
    <button class="primary" disabled={busy} onclick={() => decide(true)}>Approve and run</button>
    <button disabled={busy} onclick={() => decide(false)}>Reject</button>
  </div>
</section>

<style>
  .approval {
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
