<script lang="ts">
  // Nodes a change works on: reopen a node for edition, move it through its
  // lifecycle, edit its properties, and see which ones must still leave an
  // editable state before the change can be applied (ADR 0014).
  import type { GraphNode, LifecycleTransition } from '../api';
  import { isReopen, type LifecycleRow } from '../lifecycle';

  let {
    rows,
    candidates,
    disabled = false,
    busy = '',
    onmove,
    onedit,
    onadd,
  }: {
    rows: LifecycleRow[];
    /** nodes the change could take on */
    candidates: GraphNode[];
    disabled?: boolean;
    busy?: string;
    onmove: (row: LifecycleRow, t: LifecycleTransition) => void;
    /** proposes new values for properties (only the changed ones) */
    onedit: (row: LifecycleRow, patch: Record<string, unknown>) => Promise<boolean> | boolean;
    onadd: (id: string) => void;
  } = $props();

  let picked = $state('');
  let filter = $state('');
  let editing = $state('');
  let draft = $state<Record<string, string>>({});
  let newKey = $state('');
  let newValue = $state('');
  let formError = $state('');

  const shown = $derived(
    candidates.filter((n) => !filter || `${n.key} ${n.type}`.toLowerCase().includes(filter.toLowerCase())).slice(0, 200),
  );
  const leftEditable = $derived(rows.filter((r) => r.lifecycle && r.editable));

  const text = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v));

  /** Property names of the form: declared ones first, then the ones the node has. */
  function fields(r: LifecycleRow): string[] {
    return [...new Set([...r.declared, ...Object.keys(r.props)])];
  }

  function startEdit(r: LifecycleRow) {
    editing = r.node.id ?? '';
    draft = Object.fromEntries(fields(r).map((k) => [k, text(r.props[k])]));
    newKey = newValue = formError = '';
  }

  /** A value typed in the form: text, unless the property already holds a non-text value. */
  function parse(r: LifecycleRow, key: string, value: string): unknown {
    const cur = r.props[key];
    if (cur !== undefined && cur !== null && typeof cur !== 'string') {
      try {
        return JSON.parse(value);
      } catch {
        throw new Error(`“${key}” holds ${typeof cur === 'object' ? 'structured' : typeof cur} data: ${value} is not valid JSON`);
      }
    }
    return value;
  }

  async function save(r: LifecycleRow) {
    formError = '';
    const patch: Record<string, unknown> = {};
    try {
      for (const [k, v] of Object.entries(draft)) if (v !== text(r.props[k])) patch[k] = parse(r, k, v);
      if (newKey.trim()) {
        if (newKey.trim() in draft) throw new Error(`property “${newKey.trim()}” is already in the form`);
        patch[newKey.trim()] = newValue;
      }
    } catch (e) {
      formError = e instanceof Error ? e.message : String(e);
      return;
    }
    if (!Object.keys(patch).length) {
      editing = '';
      return;
    }
    if (await onedit(r, patch)) editing = '';
  }

  function add() {
    if (!picked) return;
    onadd(picked);
    picked = '';
  }
</script>

<section class="card" id="change-lifecycle">
  <h3>Nodes <span class="count">{rows.length}</span></h3>
  <p class="hint">
    A node is modified only in an editable state, which it holds only through a change: reopen it, edit it, then move it to a
    non-editable state before applying. Nodes without a lifecycle can be edited directly.
  </p>
  {#if leftEditable.length}
    <div class="alert" role="status">
      {leftEditable.map((r) => r.node.key).join(', ')} {leftEditable.length > 1 ? 'are' : 'is'} still in an editable state: move
      {leftEditable.length > 1 ? 'them' : 'it'} out of it before applying the change.
    </div>
  {/if}

  {#if rows.length}
    <table>
      <thead><tr><th>Node</th><th>State</th><th>Actions</th></tr></thead>
      <tbody>
        {#each rows as r (r.node.id)}
          <tr>
            <td>
              <code>{r.node.key}</code> <span class="hint">{r.node.type} v{r.node.version ?? 0}</span>
              {#if r.edits}<span class="tag ok" title="Property edits proposed in this change">{r.edits} edit{r.edits > 1 ? 's' : ''}</span>{/if}
            </td>
            <td>
              {#if r.lifecycle}
                <span class="state" class:editable={r.editable}>{r.effective}</span>
                {#if r.moves.length}
                  <span class="hint" title="Proposed in this change">from {r.base || 'no state'} → {r.moves.join(' → ')}</span>
                {/if}
                {#if r.editable}<span class="tag">editable</span>{/if}
              {:else}
                <span class="hint">no lifecycle</span>
              {/if}
            </td>
            <td class="actions">
              {#if disabled}
                <span class="hint">change closed</span>
              {:else}
                <button
                  type="button"
                  class="small"
                  disabled={!r.editable || busy !== ''}
                  title={r.editable ? 'Edit the properties' : 'Reopen the node to edit it'}
                  onclick={() => (editing === r.node.id ? (editing = '') : startEdit(r))}
                >
                  Edit
                </button>
                {#each r.transitions as t (t.name)}
                  {@const reopen = isReopen(r, t)}
                  <button
                    type="button"
                    class="small"
                    class:primary={reopen}
                    disabled={busy !== ''}
                    title={`${t.name}: ${t.from} → ${t.to}${t.permission ? ` (needs ${t.permission})` : ''}`}
                    onclick={() => onmove(r, t)}
                  >
                    {reopen ? `Reopen → ${t.to}` : `${t.name} → ${t.to}`}
                  </button>
                {:else}
                  {#if r.lifecycle}
                    <span class="hint">{r.lifecycle.states?.find((s) => s.name === r.effective)?.final ? 'final state' : 'no transition from this state'}</span>
                  {/if}
                {/each}
              {/if}
            </td>
          </tr>
          {#if editing === r.node.id && !disabled}
            <tr class="editrow">
              <td colspan="3">
                <form
                  class="edit"
                  onsubmit={(e) => {
                    e.preventDefault();
                    void save(r);
                  }}
                >
                  {#each Object.keys(draft) as k (k)}
                    <div class="field">
                      <label for="p-{r.node.id}-{k}">{k}{#if !r.declared.includes(k)} <span class="hint">(not declared by {r.node.type})</span>{/if}</label>
                      {#if draft[k].length > 80 || draft[k].includes('\n')}
                        <textarea id="p-{r.node.id}-{k}" rows="4" bind:value={draft[k]}></textarea>
                      {:else}
                        <input id="p-{r.node.id}-{k}" type="text" bind:value={draft[k]} />
                      {/if}
                    </div>
                  {/each}
                  <div class="newprop">
                    <input type="text" class="mono" placeholder="new property" aria-label="New property name" bind:value={newKey} />
                    <input type="text" placeholder="value" aria-label="New property value" bind:value={newValue} />
                  </div>
                  <p class="hint">
                    Values are text; a property that already holds a number, boolean or list is edited as JSON. Only the changed
                    properties are proposed.
                  </p>
                  {#if formError}<div class="alert">{formError}</div>{/if}
                  <div class="row">
                    <button type="submit" class="primary small" disabled={busy !== ''}>{busy === `${r.node.id}:edit` ? 'Proposing…' : 'Propose the changes'}</button>
                    <button type="button" class="small" onclick={() => (editing = '')}>Cancel</button>
                  </div>
                </form>
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="empty">No node in this change yet: add one below.</p>
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
  .tag.ok {
    color: var(--ok);
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
  .editrow td {
    background: var(--surface-2);
  }
  .edit {
    display: grid;
    gap: 0.4rem;
    padding: 0.4rem 0.2rem;
  }
  .newprop {
    display: grid;
    grid-template-columns: minmax(8rem, 1fr) 3fr;
    gap: 0.4rem;
  }
</style>
