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
      // PermissionDenied : l'utilisateur courant n'a pas la permission requise
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<section class="card approval">
  <h3>Approbation requise</h3>
  <p>
    L'action <code>{task?.action}</code> nécessite la permission <code>{task?.permission}</code>, que
    l'initiateur du processus ne possède pas. Une personne habilitée doit l'approuver.
  </p>
  {#if task?.description}<p class="hint">{task.description}</p>{/if}
  <label class="field">
    <span>Commentaire (facultatif)</span>
    <textarea rows="2" bind:value={comment}></textarea>
  </label>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="row">
    <button class="primary" disabled={busy} onclick={() => decide(true)}>Approuver et exécuter</button>
    <button disabled={busy} onclick={() => decide(false)}>Refuser</button>
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
