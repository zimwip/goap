<script lang="ts">
  // Adopt / Discard buttons of an open flow branch, available while the branch is open.
  import { errorMessage, type Flow } from '../api';
  import { adoptBlockedReason, decideFlow, processOfFlow, staleCount } from '../flowDecision';

  let {
    flow,
    changeId,
    ondecided,
    compact = false,
  }: { flow: Flow; changeId: string; ondecided?: () => void; compact?: boolean } = $props();

  let busy = $state(false);
  let error = $state('');
  let confirming = $state(false);
  let comment = $state('');

  const blocked = $derived(adoptBlockedReason(flow, processOfFlow(flow)));

  async function run(adopt: boolean) {
    busy = true;
    error = '';
    try {
      await decideFlow(flow, changeId, adopt, comment.trim());
      confirming = false;
      comment = '';
      ondecided?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if flow.status === 'open'}
  <span class="flow-actions" class:compact>
    <button type="button" class="primary" disabled={busy || !!blocked} title={blocked || 'Adopt this flow: its items count, the replaced outputs become superseded'} onclick={() => run(true)}>
      Adopt
    </button>
    <button type="button" class="danger" disabled={busy} onclick={() => (confirming = !confirming)}>Discard branch</button>
    {#if blocked && !compact}<span class="hint why">Adopt unavailable: {blocked}</span>{/if}
  </span>
  {#if confirming}
    <div class="confirm card" role="alertdialog" aria-label="Discard this flow branch">
      <p>
        Discard this branch? Its candidate items are rejected, the
        <strong>{staleCount(flow)}</strong> stale item{staleCount(flow) === 1 ? '' : 's'} count again{#if flow.branch}, and the graph
        branch <code>{flow.branch}</code> is abandoned{/if}. The previous run stays as it was.
      </p>
      <label class="field">
        <span>Comment (optional)</span>
        <input type="text" bind:value={comment} />
      </label>
      <div class="row">
        <button type="button" class="danger" disabled={busy} onclick={() => run(false)}>Discard branch</button>
        <button type="button" disabled={busy} onclick={() => (confirming = false)}>Keep it</button>
      </div>
    </div>
  {/if}
  {#if error}<div class="alert">{error}</div>{/if}
{/if}

<style>
  .flow-actions {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .why {
    font-size: 0.85em;
  }
  .danger {
    color: var(--danger);
    border-color: var(--danger);
  }
  .confirm {
    margin: 6px 0;
    padding: 8px 10px;
    border-left: 3px solid var(--danger);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 6px 0;
  }
  .row {
    display: flex;
    gap: 6px;
  }
</style>
