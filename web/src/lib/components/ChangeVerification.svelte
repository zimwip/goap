<script lang="ts">
  // Verification of a change (ADR 0075 §1): per effect of an action run, who produced it, the kind of oracle it needs,
  // whether the verifier must differ from the producer, and the state it reached (a result produced is not a result
  // accepted). Read only: the states are facts the engine writes as the actions run and the reviews settle.
  import type { Change } from '../api';
  import { verifications } from '../verification';

  let { change }: { change: Change } = $props();

  const rows = $derived(verifications(change.items ?? []));
  const open = $derived(rows.filter((r) => r.open).length);
  const reserves = $derived(rows.filter((r) => r.state === 'accepted_with_reserve').length);
</script>

<section class="card">
  <h3>
    Verification
    <span class="hint">{open} awaiting a verdict · {reserves} accepted with reserve · {rows.length} in all</span>
  </h3>
  {#if rows.length}
    <table class="reg">
      <thead><tr><th>Effect</th><th>Action</th><th>Produced by</th><th>Oracle</th><th>State</th><th>Verified by</th><th>Derogation</th></tr></thead>
      <tbody>
        {#each rows as r, i (r.execution + r.impact + i)}
          <tr class:warn={r.state === 'rejected'} class:dim={r.state === 'accepted'}>
            <td class="mono">{r.impact ? r.impact.slice(0, 8) : '—'}</td>
            <td class="mono">{r.action}</td>
            <td>{r.producer || '—'}</td>
            <td>{r.oracle || '—'}{#if r.oracle && r.independent}<span class="hint"> · independent</span>{/if}</td>
            <td><span class="state {r.state}">{r.state.replaceAll('_', ' ')}</span></td>
            <td>{r.by || '—'}</td>
            <td class="mono">{r.derogation || '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="empty">No action run declared a verification on this change.</p>
  {/if}
</section>

<style>
  .reg {
    width: 100%;
    border-collapse: collapse;
  }
  .reg th,
  .reg td {
    text-align: left;
    padding: 3px 6px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  tr.dim {
    opacity: 0.7;
  }
  tr.warn td:first-child {
    border-left: 3px solid var(--danger);
  }
  .state.accepted_with_reserve {
    color: var(--warn);
  }
  .state.rejected {
    color: var(--danger);
  }
  .state.accepted {
    color: var(--ok);
  }
</style>
