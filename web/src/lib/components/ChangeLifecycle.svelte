<script lang="ts">
  // Lifecycle of the nodes a change works on: reopen a node for edition, move it
  // through its lifecycle, and see which ones must still leave an editable state
  // before the change can be applied (ADR 0014).
  import type { GraphNode, LifecycleTransition } from '../api';
  import { isReopen, type LifecycleRow } from '../lifecycle';

  let {
    rows,
    candidates,
    disabled = false,
    busy = '',
    onmove,
    onadd,
  }: {
    rows: LifecycleRow[];
    /** nodes with a lifecycle the change could take on */
    candidates: GraphNode[];
    disabled?: boolean;
    busy?: string;
    onmove: (row: LifecycleRow, t: LifecycleTransition) => void;
    onadd: (id: string) => void;
  } = $props();

  let picked = $state('');
  let filter = $state('');

  const shown = $derived(
    candidates.filter((n) => !filter || `${n.key} ${n.type}`.toLowerCase().includes(filter.toLowerCase())).slice(0, 200),
  );
  const leftEditable = $derived(rows.filter((r) => r.editable));

  function add() {
    if (!picked) return;
    onadd(picked);
    picked = '';
  }
</script>

<section class="card" id="change-lifecycle">
  <h3>Lifecycle <span class="count">{rows.length}</span></h3>
  <p class="hint">
    A node is modified only in an editable state, which it holds only through a change: reopen it, edit it, then move it to a
    non-editable state before applying.
  </p>
  {#if leftEditable.length}
    <div class="alert" role="status">
      {leftEditable.map((r) => r.node.key).join(', ')} {leftEditable.length > 1 ? 'are' : 'is'} still in an editable state: move
      {leftEditable.length > 1 ? 'them' : 'it'} out of it before applying the change.
    </div>
  {/if}

  {#if rows.length}
    <table>
      <thead><tr><th>Node</th><th>State</th><th>Move to</th></tr></thead>
      <tbody>
        {#each rows as r (r.node.id)}
          <tr>
            <td><code>{r.node.key}</code> <span class="hint">{r.node.type} v{r.node.version ?? 0}</span></td>
            <td>
              <span class="state" class:editable={r.editable}>{r.effective}</span>
              {#if r.moves.length}
                <span class="hint" title="Proposed in this change">from {r.base || 'no state'} → {r.moves.join(' → ')}</span>
              {/if}
              {#if r.editable}<span class="tag">editable</span>{/if}
            </td>
            <td class="actions">
              {#if disabled}
                <span class="hint">change closed</span>
              {:else}
              {#each r.transitions as t (t.name)}
                {@const reopen = isReopen(r, t)}
                <button
                  type="button"
                  class="small"
                  class:primary={reopen}
                  disabled={disabled || busy !== ''}
                  title={`${t.name}: ${t.from} → ${t.to}${t.permission ? ` (needs ${t.permission})` : ''}`}
                  onclick={() => onmove(r, t)}
                >
                  {reopen ? `Reopen → ${t.to}` : `${t.name} → ${t.to}`}
                </button>
              {:else}
                <span class="hint">{r.lifecycle.states?.find((s) => s.name === r.effective)?.final ? 'final state' : 'no transition from this state'}</span>
              {/each}
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="empty">No node with a lifecycle in this change yet.</p>
  {/if}

  {#if !disabled && candidates.length}
    <div class="add row">
      <input type="search" placeholder="Find a node to work on…" aria-label="Filter nodes" bind:value={filter} />
      <select aria-label="Node to add" bind:value={picked}>
        <option value="">— node —</option>
        {#each shown as n (n.id)}<option value={n.id}>{n.key} ({n.type}{n.state ? `, ${n.state}` : ''})</option>{/each}
      </select>
      <button type="button" disabled={!picked} onclick={add}>Add to the change</button>
    </div>
  {/if}
</section>

<style>
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.85rem;
    font-family: var(--mono);
  }
  .state.editable {
    border-color: var(--warn);
    color: var(--warn);
  }
  .tag {
    margin-left: 0.3rem;
    font-size: 0.75rem;
    color: var(--warn);
  }
  .row {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin-top: 0.7rem;
  }
  .add input[type='search'] {
    width: 16rem;
  }
  .add select {
    width: auto;
    min-width: 16rem;
  }
</style>
