<script lang="ts">
  // Nodes a change works on: edit them in any state, move them through their
  // lifecycle, and see which ones must still reach a landable state before the
  // change can be applied (ADR 0078). With `impacts`,
  // each row is also the change impact of its node (ADR 0024): why, the versions
  // it starts from and writes, its review — one list, no second table.
  import type { GraphNode, LifecycleTransition } from '../api';
  import { birthStates, type LifecycleRow } from '../lifecycle';
  import type { Lifecycle } from '../api';
  import StatusBadge from './StatusBadge.svelte';
  import { confirmDialog } from '../shell/confirmState.svelte';

  let {
    rows,
    candidates,
    disabled = false,
    busy = '',
    onmove,
    onedit,
    onadd,
    types,
    lifecycleOf,
    keys,
    oncreate,
    onremove,
    onhistory,
    onopennode,
    impacts = false,
    scope = '',
    onreview,
  }: {
    rows: LifecycleRow[];
    /** nodes the change could take on */
    candidates: GraphNode[];
    disabled?: boolean;
    busy?: string;
    onmove: (row: LifecycleRow, t: LifecycleTransition) => void;
    /** proposes new values for properties (only the changed ones) */
    onedit: (row: LifecycleRow, patch: Record<string, unknown>, state?: string) => Promise<boolean> | boolean;
    /** takes on a node of the baseline; with impacts, rationale says why the change impacts it */
    onadd: (id: string, rationale?: string) => void;
    /** node types a new node can have */
    types: string[];
    lifecycleOf: (type: string) => Lifecycle | undefined;
    /** keys already taken (baseline and pending nodes) */
    keys: string[];
    /** creates a node: identity and type only, the rest comes from Edit */
    oncreate: (key: string, type: string, state: string) => Promise<boolean> | boolean;
    /** takes a node out of the change (a node the change creates is discarded) */
    onremove: (row: LifecycleRow) => Promise<boolean> | boolean;
    /** opens the history of a stored node */
    onhistory: (row: LifecycleRow) => void;
    /** opens the node editor on a stored node */
    onopennode: (row: LifecycleRow) => void;
    /** show each row as the change impact of its node: why, versions, review */
    impacts?: boolean;
    /** the flow the rows are the view of: 'main' or an option id (ADR 0032 §6) */
    scope?: string;
    /** accepts or rejects the change impact of a row; the comment is mandatory */
    onreview?: (row: LifecycleRow, accept: boolean, comment: string) => Promise<boolean> | boolean;
  } = $props();

  const onOption = $derived(impacts && !!scope && scope !== 'main');
  const version = (r?: { id?: string; version?: number }) => (r?.id ? `v${r.version}` : '—');
  let reviewing = $state('');
  let reviewComment = $state('');
  let rationale = $state('');
  async function review(r: LifecycleRow, accept: boolean) {
    if (onreview && (await onreview(r, accept, reviewComment.trim()))) {
      reviewing = '';
      reviewComment = '';
    }
  }
  const openable = (r: LifecycleRow) => !r.created || !!r.impact?.post?.id;

  async function remove(r: LifecycleRow) {
    const what = r.created
      ? `Discard the new node ${r.node.key}? It is taken out of the change.`
      : `Take ${r.node.key} (${r.node.type}) out of this change? Its working version is dropped; a node is never deleted (ADR 0076).`;
    if (await confirmDialog({ message: what, danger: true })) void onremove(r);
  }

  let newNodeKey = $state('');
  let newNodeType = $state('');
  let newNodeState = $state('');
  let createError = $state('');
  let creating = $state(false);
  let draftState = $state('');

  const newLifecycle = $derived(newNodeType ? lifecycleOf(newNodeType) : undefined);
  const newStates = $derived(birthStates(newLifecycle));

  $effect(() => {
    // a type change resets the state to its initial one
    newNodeState = newLifecycle?.initial ?? '';
  });

  async function create() {
    createError = '';
    const key = newNodeKey.trim();
    if (!key) return void (createError = 'A node needs a key.');
    if (!newNodeType) return void (createError = 'Choose the node type.');
    if (keys.includes(key)) return void (createError = `The key “${key}” is already used.`);
    creating = true;
    try {
      if (await oncreate(key, newNodeType, newNodeState)) {
        newNodeKey = '';
        newNodeType = '';
      }
    } finally {
      creating = false;
    }
  }

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
  const stuck = $derived(rows.filter((r) => r.lifecycle && !r.landable));

  const text = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v));

  /** Property names of the form: declared ones first, then the ones the node has. */
  function fields(r: LifecycleRow): string[] {
    return [...new Set([...r.declared, ...Object.keys(r.props)])];
  }

  function startEdit(r: LifecycleRow) {
    editing = r.node.id ?? '';
    draftState = r.effective;
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
    const stateChanged = !!r.created && !!r.lifecycle && draftState !== r.effective;
    if (!Object.keys(patch).length && !stateChanged) {
      editing = '';
      return;
    }
    if (await onedit(r, patch, stateChanged ? draftState : undefined)) editing = '';
  }

  function add() {
    if (!picked) return;
    onadd(picked, rationale.trim());
    picked = '';
    rationale = '';
  }
</script>

<section class="card" id="change-lifecycle">
  {#if !impacts}<h3>Nodes <span class="count">{rows.length}</span></h3>{/if}
  <details class="rules">
    <summary class="hint">How nodes are edited</summary>
    <p class="hint">
      A node is edited in any state while it is in a change. A state flagged not landable keeps the change from landing: move the
      node to a landable state before applying.
    </p>
  </details>
  {#if stuck.length}
    <div class="alert" role="status">
      {stuck.map((r) => `${r.node.key} (${r.effective})`).join(', ')} {stuck.length > 1 ? 'are' : 'is'} in a state that cannot land: move
      {stuck.length > 1 ? 'them' : 'it'} to a landable state before applying the change.
    </div>
  {/if}

  {#if rows.length}
    <table>
      <thead><tr><th>Node</th>{#if impacts}<th>Why</th><th>Versions</th>{/if}<th>State</th>{#if impacts}<th>Review</th>{/if}<th>Actions</th></tr></thead>
      <tbody>
        {#each rows as r (r.node.id)}
          <tr>
            <td>
              {#if !openable(r)}<code>{r.node.key}</code>{:else}<button type="button" class="link mono" title="Open the node in its editor, as this change has it" onclick={() => onopennode(r)}>{r.node.key}</button>{/if}
              <span class="hint">{r.node.type}{r.created ? '' : ` v${r.node.version ?? 0}`}</span>
              {#if r.created}<span class="tag ok" title="Created by this change; stored when it is applied">new</span>{/if}
              {#if r.edits}<span class="tag ok" title="Property edits proposed in this change">{r.edits} edit{r.edits > 1 ? 's' : ''}</span>{/if}
              {#if onOption && r.impact}
                {#if r.impact.flow === scope}<span class="origin own" title="declared by this option: it lands only if the option is selected">this option</span>{:else}<span class="origin" title="declared on the main flow: every option sees it">main flow</span>{/if}
              {/if}
            </td>
            {#if impacts}
              <td class="why">{r.impact?.rationale ?? ''}{#if r.impact && !r.impact.post}<span class="hint planned" title="declared, no version written yet">planned</span>{/if}</td>
              <td class="mono">
                {#if r.impact}{version(r.impact.pre)} → {version(r.impact.post)}{#if r.impact.landed?.id}<span class="hint"> · landed {version(r.impact.landed)}</span>{/if}{:else}—{/if}
              </td>
            {/if}
            <td>
              {#if r.lifecycle}
                <span class="state" class:notLandable={!r.landable}>{r.effective}</span>
                {#if r.moves.length}
                  <span class="hint" title="Proposed in this change">from {r.base || 'no state'} → {r.moves.join(' → ')}</span>
                {/if}
                {#if !r.landable}<span class="tag" title="A change cannot land with the node in this state">{r.created ? 'born not landable: choose another state' : 'not landable'}</span>{/if}
              {:else}
                <span class="hint">no lifecycle</span>
              {/if}
            </td>
            {#if impacts}
              <td>
                {#if r.impact}
                  <StatusBadge status={r.impact.review} />
                  {#if r.impact.reviews?.length}<div class="hint">{r.impact.reviews.at(-1)?.by}: {r.impact.reviews.at(-1)?.comment}</div>{/if}
                  {#if !disabled && onreview && r.impact.review === 'proposed' && !r.impact.superseded}
                    <button type="button" class="link small" onclick={() => ((reviewing = reviewing === r.impact?.id ? '' : (r.impact?.id ?? '')), (reviewComment = ''))}>Review…</button>
                  {/if}
                {/if}
              </td>
            {/if}
            <td class="actions">
              {#if disabled}
                <span class="hint">change closed</span>
              {:else}
                {#if !r.created}
                  <button type="button" class="small ghost" title="Versions and states of the node" onclick={() => onhistory(r)}>History</button>
                {/if}
                <button
                  type="button"
                  class="small"
                  disabled={busy !== ''}
                  title="Edit the properties"
                  onclick={() => (editing === r.node.id ? (editing = '') : startEdit(r))}
                >
                  Edit
                </button>
                {#each r.transitions as t (t.name)}
                  <button
                    type="button"
                    class="small"
                    disabled={busy !== ''}
                    title={`${t.name}: ${t.from} → ${t.to}${t.permission ? ` (needs ${t.permission})` : ''}`}
                    onclick={() => onmove(r, t)}
                  >
                    {t.name} → {t.to}
                  </button>
                {:else}
                  {#if r.lifecycle}
                    <span class="hint">{r.lifecycle.states?.find((s) => s.name === r.effective)?.final ? 'final state' : 'no transition from this state'}</span>
                  {/if}
                {/each}
                {#if r.impact && !r.impact.superseded}
                  <button
                    type="button"
                    class="small danger"
                    disabled={busy !== ''}
                    title={r.created ? 'Discard this new node' : 'Take the node out of the change (refused once a version of it is checked in: reject it instead)'}
                    onclick={() => remove(r)}
                  >
                    {r.created ? 'Discard' : 'Remove from change'}
                  </button>
                {/if}
              {/if}
            </td>
          </tr>
          {#if impacts && reviewing && reviewing === r.impact?.id && !disabled}
            <tr class="editrow">
              <td colspan="6">
                <div class="row review">
                  <input type="text" class="grow" placeholder="Comment (mandatory)" aria-label="Review comment" bind:value={reviewComment} />
                  <button type="button" class="small primary" disabled={!reviewComment.trim() || busy !== ''} onclick={() => review(r, true)}>Accept</button>
                  <button type="button" class="small danger" disabled={!reviewComment.trim() || busy !== ''} onclick={() => review(r, false)}>Reject</button>
                  <button type="button" class="small" onclick={() => (reviewing = '')}>Cancel</button>
                </div>
              </td>
            </tr>
          {/if}
          {#if editing === r.node.id && !disabled}
            <tr class="editrow">
              <td colspan={impacts ? 6 : 3}>
                <form
                  class="edit"
                  onsubmit={(e) => {
                    e.preventDefault();
                    void save(r);
                  }}
                >
                  {#if r.created && r.lifecycle}
                    <div class="field">
                      <label for="st-{r.node.id}">State the node is created in</label>
                      <select id="st-{r.node.id}" bind:value={draftState}>
                        {#each birthStates(r.lifecycle) as s (s)}<option value={s}>{s}</option>{/each}
                      </select>
                    </div>
                  {/if}
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

  {#if !disabled}
    <form
      class="create row"
      onsubmit={(e) => {
        e.preventDefault();
        void create();
      }}
    >
      <strong>{impacts ? 'New impact: create a node' : 'New node'}</strong>
      <input type="text" class="mono" placeholder="key (e.g. REQ-12)" aria-label="Key of the new node" bind:value={newNodeKey} />
      <select aria-label="Type of the new node" bind:value={newNodeType}>
        <option value="">— type —</option>
        {#each types as t (t)}<option value={t}>{t}</option>{/each}
      </select>
      {#if newStates.length > 1}
        <select aria-label="State of the new node" bind:value={newNodeState}>
          {#each newStates as s (s)}<option value={s}>{s}</option>{/each}
        </select>
      {/if}
      <button type="submit" disabled={creating || busy !== ''}>{creating ? 'Creating…' : 'Create'}</button>
    </form>
    {#if createError}<div class="alert">{createError}</div>{/if}
    <p class="hint">A new node starts with its key and type only: fill in its properties with Edit.</p>
  {/if}

  {#if !disabled && candidates.length}
    <div class="add row">
      <input type="search" placeholder="Find a node to work on…" aria-label="Filter nodes" bind:value={filter} />
      <select aria-label="Node to add" bind:value={picked}>
        <option value="">— node —</option>
        {#each shown as n (n.id)}<option value={n.id}>{n.key} ({n.type}{n.state ? `, ${n.state}` : ''})</option>{/each}
      </select>
      {#if impacts}<input type="text" class="why-input" placeholder="why it is impacted" aria-label="Why the change impacts the node" bind:value={rationale} />{/if}
      <button type="button" disabled={!picked || (impacts && !rationale.trim())} onclick={add}>{impacts ? 'Add the impact' : 'Add to the change'}</button>
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
  .state.notLandable {
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
  .create input[type='text'] {
    width: 12rem;
  }
  .create select {
    width: auto;
    min-width: 10rem;
  }
  .add input[type='search'] {
    width: 16rem;
  }
  .add select {
    width: auto;
    min-width: 16rem;
  }
  .rules {
    margin: 0 0 6px;
  }
  .rules summary {
    cursor: pointer;
    width: fit-content;
  }
  .rules p {
    margin: 4px 0 0;
  }
  /* a chip never breaks inside: the cell wraps between chips */
  .tag,
  .origin {
    white-space: nowrap;
    display: inline-block;
  }
  .origin {
    margin-left: 0.3rem;
    font-size: 0.75rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    color: var(--muted);
  }
  .origin.own {
    color: var(--scope, var(--accent));
    border-color: var(--scope, var(--accent));
  }
  .why {
    max-width: 22rem;
  }
  .planned {
    display: block;
    white-space: nowrap;
  }
  .add .why-input {
    width: 16rem;
  }
  .review .grow {
    flex: 1;
    min-width: 14rem;
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
