<script lang="ts">
  // Derogations of a change (ADR 0075 §2): a waiver with a rule, a signatory and an expiry, each version of the record of
  // its key kept in the change log. Signing needs derogation:sign (and the role the criticality level asks of a
  // signatory, policy of the organisation): the server enforces both and says why when it refuses.
  import { graph, errorMessage, type Change, type Struct } from '../api';
  import { derogationRegister, openDerogation, expiredDerogation, closing } from '../verification';
  import { nextKey } from '../risks';
  import { me } from '../stores/session.svelte';

  let { change, closed = false, onchange }: { change: Change; closed?: boolean; onchange?: () => void } = $props();

  const rows = $derived(derogationRegister(change.items ?? []));
  const openCount = $derived(rows.filter(openDerogation).length);
  let error = $state('');
  let busy = $state(false);
  let rule = $state('');
  let target = $state('');
  let reason = $state('');
  let expires = $state('');

  async function write(data: Struct) {
    if (!change.id) return;
    busy = true;
    error = '';
    try {
      await graph.addItems(change.id, [{ kind: 'derogation', type: 'derogation', data }]);
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  async function sign() {
    if (!rule.trim() || !target.trim() || !reason.trim() || !expires) return;
    await write({ key: nextKey('DRG-', rows.map((d) => d.key)), rule: rule.trim(), target: target.trim(), reason: reason.trim(), signatory: me(), expires });
    rule = target = reason = expires = '';
  }
</script>

<section class="card">
  <h3>Derogations <span class="hint">{openCount} open · {rows.length} in all</span></h3>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if rows.length}
    <table class="reg">
      <thead><tr><th>Key</th><th>Rule waived</th><th>Target</th><th>Reason</th><th>Signatory</th><th>Expires</th><th>Status</th></tr></thead>
      <tbody>
        {#each rows as d (d.key)}
          <tr class:dim={!openDerogation(d)} class:warn={expiredDerogation(d)}>
            <td class="mono">{d.key}</td>
            <td>{d.rule}</td>
            <td class="mono">{d.target}</td>
            <td>{d.reason}</td>
            <td>{d.signatory}</td>
            <td class="mono">{d.expires}</td>
            <td>
              {#if expiredDerogation(d)}<span class="state expired">expired</span>{:else}{d.status}{/if}
              {#if !closed && openDerogation(d)}
                <button type="button" class="small" disabled={busy} onclick={() => write(closing(d, me()))}>Close</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="empty">No derogation signed on this change.</p>
  {/if}
  {#if !closed}
    <form class="row add" onsubmit={(e) => (e.preventDefault(), sign())}>
      <input type="text" placeholder="Rule waived" bind:value={rule} aria-label="Rule waived" />
      <input type="text" placeholder="Target (impact, action, gate)" bind:value={target} aria-label="Target" />
      <input type="text" placeholder="Reason" bind:value={reason} aria-label="Reason" />
      <input type="date" bind:value={expires} aria-label="Expires" />
      <button type="submit" class="small primary" disabled={busy || !rule.trim() || !target.trim() || !reason.trim() || !expires}>Sign</button>
    </form>
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
    opacity: 0.6;
  }
  tr.warn td:first-child {
    border-left: 3px solid var(--danger);
  }
  .state.expired {
    color: var(--danger);
  }
  .add {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 8px;
    align-items: center;
  }
  .add input {
    width: auto;
    flex: 1 1 140px;
  }
  .add button {
    flex: none;
  }
</style>
