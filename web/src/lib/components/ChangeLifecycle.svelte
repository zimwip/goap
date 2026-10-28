<script lang="ts">
  // Nodes a change works on: edit them in any state, move them through their
  // lifecycle, and see which ones must still reach a committable state before the
  // change can be applied (ADR 0078). With `impacts`,
  // each row is also the change impact of its node (ADR 0024): why, the versions
  // it starts from and writes, its review — one list, no second table.
  import { isDraft, type GraphNode, type LifecycleTransition } from '../api';
  import { birthStates, type LifecycleRow } from '../lifecycle';
  import type { Lifecycle } from '../api';
  import ImpactDecisions from './ImpactDecisions.svelte';
  import { confirmDialog } from '../shell/confirmState.svelte';
  import { assistTarget } from '../assist/registry.svelte';
  import AssessmentBubble from './AssessmentBubble.svelte';
  import { impactSubject } from '../descriptionReview';
  import type { Change } from '../api';
  import ImpactDiffDialog from './ImpactDiffDialog.svelte';
  import type { LinkWrite, NodeRef } from '../api';
  import FormRenderer from '../forms/FormRenderer.svelte';
  import type { FormValues, NodeSource } from '../forms/types';
  import { nodeWrite } from '../forms/nodeForm';
  import { typeCatalog } from '../stores/types.svelte';

  let {
    rows,
    candidates,
    disabled = false,
    busy = '',
    onmove,
    onadd,
    types,
    lifecycleOf,
    keys,
    oncreate,
    onremove,
    onremovemany,
    onhistory,
    onopennode,
    impacts = false,
    scope = '',
    selected = '',
    onopenreviews,
    change,
    closed = false,
    onchanged,
    filter = $bindable(''),
  }: {
    rows: LifecycleRow[];
    /** nodes the change could take on (with `filter`, those its text matches: the caller may search them) */
    candidates: GraphNode[];
    /** the text the person looks for a node to take on with */
    filter?: string;
    disabled?: boolean;
    busy?: string;
    onmove: (row: LifecycleRow, t: LifecycleTransition) => void;
    /** takes on a node of the commit; with impacts, rationale says why the change impacts it */
    onadd: (id: string, rationale?: string) => void;
    /** node types a new node can have */
    types: string[];
    lifecycleOf: (type: string) => Lifecycle | undefined;
    /** keys already taken (commit and pending nodes) */
    keys: string[];
    /** creates a node: identity and type, with what the form of its type gave (ADR 0115 §7); the rest comes from Edit */
    oncreate: (key: string, type: string, state: string, init?: { props: Record<string, unknown>; links: LinkWrite[] }) => Promise<boolean> | boolean;
    /** takes a node out of the change (a node the change creates is discarded) */
    onremove: (row: LifecycleRow) => Promise<boolean> | boolean;
    /** takes the selected nodes out of the change at once (all of them or none); with impacts, rows get a checkbox */
    onremovemany?: (rows: LifecycleRow[]) => Promise<boolean> | boolean;
    /** opens the history of a stored node */
    onhistory: (row: LifecycleRow) => void;
    /** opens a node in the node editor, seen through the change; `edit` (the Edit of its row): the standard node editor
     * whatever its type names, a creation only planned given its draft first */
    onopennode: (row: LifecycleRow, edit?: boolean) => void;
    /** show each row as the change impact of its node: why, versions, review */
    impacts?: boolean;
    /** the flow the rows are the view of: 'main' or an option id (ADR 0032 §6) */
    scope?: string;
    /** the impact the assistant (or the person) points at: outlined in the table */
    selected?: string;
    /** opens the Reviews tab, where the impacts are reviewed */
    onopenreviews?: () => void;
    /** with impacts: the change, whose assessments of the impacts (`impact:<key>`, ADR 0110) are shown by their node */
    change?: Change;
    /** the change can no longer be answered */
    closed?: boolean;
    /** an answer to an assessment was added */
    onchanged?: () => void;
  } = $props();

  // an assessment of an impact is processed once the impact is decided by its review
  const decided = (r: LifecycleRow) => r.impact?.review === 'accepted' || r.impact?.review === 'rejected';

  const onOption = $derived(impacts && !!scope && scope !== 'main');
  // a draft reference has no version (ADR 0079): the node gets one when the change commits
  const version = (r?: { id?: string; version?: number }) => (r?.id ? (isDraft(r) ? 'draft' : `v${r.version}`) : '—');
  let rationale = $state('');
  // Reviews are never automatic (ADR 0079): an edit leaves its impact proposed, and the graph refuses to commit the change
  // while one awaits its review.
  const awaiting = $derived(impacts ? rows.filter((r) => r.impact && !r.impact.superseded && r.impact.post?.id && (r.impact.review === 'proposed' || !r.impact.review)) : []);
  const openable = (r: LifecycleRow) => !r.created || !!r.impact?.post?.id;
  // the row whose Diff dialog is open: what the change modifies of its node (read-only, closed changes too)
  let diffing = $state<LifecycleRow | undefined>();
  const diffable = (r: LifecycleRow) => impacts && !!change?.id && !!r.impact?.post?.id;

  async function remove(r: LifecycleRow) {
    const what = r.created
      ? `Discard the new node ${r.node.key}? It is taken out of the change.`
      : `Take ${r.node.key} (${r.node.type}) out of this change? Its draft is dropped; a node is never deleted (ADR 0076).`;
    if (await confirmDialog({ message: what, danger: true })) void onremove(r);
  }

  // Bulk withdraw: check the rows, then take them out of the change in one go, after a confirmation.
  const removable = (r: LifecycleRow) => !!r.impact?.id && !r.impact.superseded;
  const bulk = $derived(impacts && !!onremovemany && !disabled);
  let checked = $state<string[]>([]);
  const removableRows = $derived(rows.filter(removable));
  const checkedRows = $derived(removableRows.filter((r) => checked.includes(r.impact?.id ?? '')));
  const allChecked = $derived(removableRows.length > 0 && checkedRows.length === removableRows.length);

  function toggle(r: LifecycleRow, on: boolean) {
    const id = r.impact?.id ?? '';
    checked = on ? [...checked.filter((c) => c !== id), id] : checked.filter((c) => c !== id);
  }

  function toggleAll(on: boolean) {
    checked = on ? removableRows.map((r) => r.impact?.id ?? '') : [];
  }

  async function removeChecked() {
    const picked = checkedRows;
    if (!picked.length || !onremovemany) return;
    const created = picked.filter((r) => r.created).length;
    const what =
      `Take ${picked.length} node${picked.length > 1 ? 's' : ''} out of this change? ` +
      `${picked.map((r) => r.node.key).join(', ')}. Their drafts are dropped` +
      (created ? `; ${created} new node${created > 1 ? 's are' : ' is'} discarded` : '') +
      '; a node is never deleted (ADR 0076).';
    if (!(await confirmDialog({ message: what, danger: true }))) return;
    if (await onremovemany(picked)) checked = [];
  }

  let newNodeKey = $state('');
  let newNodeType = $state('');
  let newNodeState = $state('');
  let createError = $state('');
  let creating = $state(false);

  const newLifecycle = $derived(newNodeType ? lifecycleOf(newNodeType) : undefined);
  const newStates = $derived(birthStates(newLifecycle));

  $effect(() => {
    // a type change resets the state to its initial one
    newNodeState = newLifecycle?.initial ?? '';
  });

  // the creation form of the chosen type (ADR 0115 §7): its link fields offer the nodes of the change and the candidates
  const typeForm = $derived(newNodeType ? typeCatalog.cat.type(newNodeType)?.form : undefined);
  const refs = $derived.by(() => {
    const out = new Map<string, NodeRef>();
    for (const n of candidates) if (n.key && n.id) out.set(n.key, { id: n.id, version: n.version ?? 0 });
    for (const r of rows) {
      const ref = r.impact?.post?.id ? r.impact.post : r.node.id ? { id: r.node.id, version: r.node.version ?? 0 } : undefined;
      if (r.node.key && ref) out.set(r.node.key, ref);
    }
    return out;
  });
  const formSource = $derived.by((): NodeSource => {
    const cat = typeCatalog.cat;
    const nodes = new Map<string, NodeSource['nodes'][number]>();
    for (const n of candidates) if (n.key) nodes.set(n.key, { key: n.key, type: n.type ?? '', state: n.state, namespace: n.namespace });
    for (const r of rows) if (r.node.key) nodes.set(r.node.key, { key: r.node.key, type: r.node.type ?? '', state: r.node.state, inChange: true });
    return {
      nodes: [...nodes.values()].filter((n) => refs.has(n.key)).sort((a, b) => a.key.localeCompare(b.key)),
      isA: (t, a) => t === a || cat.typesOf(t).includes(a),
      linkTypesFrom: () => (newNodeType ? cat.linksFrom(newNodeType).map((l) => l.ref ?? '').filter(Boolean) : []),
      linkLabel: (t) => cat.links.find((l) => l.ref === t)?.label ?? '',
    };
  });

  async function create(values?: FormValues) {
    createError = '';
    const key = newNodeKey.trim();
    if (!key) return void (createError = 'A node needs a key.');
    if (!newNodeType) return void (createError = 'Choose the node type.');
    if (keys.includes(key)) return void (createError = `The key “${key}” is already used.`);
    let init: { props: Record<string, unknown>; links: LinkWrite[] } | undefined;
    if (typeForm && values) {
      const w = nodeWrite(typeForm, values);
      init = { props: w.props, links: w.links.flatMap((l) => (refs.has(l.to) ? [{ type: l.type, to: refs.get(l.to)! }] : [])) };
    }
    creating = true;
    try {
      if (await oncreate(key, newNodeType, newNodeState, init)) {
        newNodeKey = '';
        newNodeType = '';
      }
    } finally {
      creating = false;
    }
  }

  let picked = $state('');

  const shown = $derived(
    candidates.filter((n) => !filter || `${n.key} ${n.type}`.toLowerCase().includes(filter.toLowerCase())).slice(0, 200),
  );
  const stuck = $derived(rows.filter((r) => r.lifecycle && !r.committable));

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
      A node is edited in any state while it is in a change. A state flagged not committable keeps the change from commit: move the
      node to a committable state before applying.
    </p>
  </details>
  {#if stuck.length}
    <div class="alert" role="status">
      {stuck.map((r) => `${r.node.key} (${r.effective})`).join(', ')} {stuck.length > 1 ? 'are' : 'is'} in a state that cannot commit: move
      {stuck.length > 1 ? 'them' : 'it'} to a committable state before applying the change.
    </div>
  {/if}

  {#if awaiting.length}
    <div class="alert" role="status" id="awaiting-review">
      <strong>Awaiting review: {awaiting.map((r) => r.node.key).join(', ')}.</strong>
      The change cannot commit while an impact awaits its review.
      {#if onopenreviews}<button type="button" class="link" onclick={onopenreviews}>Review them in the Reviews tab</button>{/if}
    </div>
  {/if}

  {#if rows.length}
    {#if bulk && removableRows.length}
      <div class="bulk" role="toolbar" aria-label="Selected impacts">
        <span class="hint">{checkedRows.length ? `${checkedRows.length} selected` : 'Select impacts to act on several at once'}</span>
        <button
          type="button"
          class="small danger"
          disabled={busy !== '' || !checkedRows.length}
          title="Take the selected nodes out of the change, all at once: ones committed already are reverted by the next commit"
          onclick={removeChecked}
        >
          Remove selected from change
        </button>
        {#if checkedRows.length}<button type="button" class="small ghost" onclick={() => toggleAll(false)}>Clear selection</button>{/if}
      </div>
    {/if}
    <table>
      <thead><tr>{#if bulk}<th class="pick"><input type="checkbox" aria-label="Select every impact" disabled={!removableRows.length} checked={allChecked} onchange={(e) => toggleAll(e.currentTarget.checked)} /></th>{/if}<th>Node</th>{#if impacts}<th>Why</th><th>Versions</th>{/if}<th>State</th>{#if impacts}<th>Review</th>{/if}<th>Actions</th></tr></thead>
      <tbody>
        {#each rows as r (r.node.id)}
          <tr use:assistTarget={`impact:${r.impact?.id ?? ''}`} class:selected={!!selected && r.impact?.id === selected} data-impact={r.impact?.id}>
            {#if bulk}
              <td class="pick">
                {#if removable(r)}<input type="checkbox" aria-label={`Select ${r.node.key}`} checked={checked.includes(r.impact?.id ?? '')} onchange={(e) => toggle(r, e.currentTarget.checked)} />{/if}
              </td>
            {/if}
            <td>
              {#if !openable(r)}<code>{r.node.key}</code>{:else}<button type="button" class="link mono" title="Open the node in its editor, as this change has it" onclick={() => onopennode(r)}>{r.node.key}</button>{/if}
              <span class="hint">{r.node.type}{r.created ? '' : ` v${r.node.version ?? 0}`}</span>
              {#if impacts && change && r.node.key}
                <AssessmentBubble {change} subject={impactSubject(r.node.key)} label="the impact on {r.node.key}" compact processed={decided(r)} {closed} onchange={onchanged} />
              {/if}
              {#if r.created}<span class="tag ok" title="Created by this change; stored when it is applied">new</span>{/if}
              {#if r.draft && !r.created}<span class="tag ok" title="The change holds a draft of the node: it gets its next version when the change commits (ADR 0079)">draft</span>{/if}
              {#if onOption && r.impact}
                {#if r.impact.flow === scope}<span class="origin own" title="declared by this option: it commits only if the option is selected">this option</span>{:else}<span class="origin" title="declared on the main flow: every option sees it">main flow</span>{/if}
              {/if}
            </td>
            {#if impacts}
              <td class="why">{r.impact?.rationale ?? ''}{#if r.impact && !r.impact.post}<span class="hint planned" title="declared, no draft yet">planned</span>{/if}</td>
              <td class="mono">
                {#if r.impact}{version(r.impact.pre)} → {version(r.impact.post)}{#if r.impact.committed?.id}<span class="hint"> · committed {version(r.impact.committed)}</span>{/if}{:else}—{/if}
              </td>
            {/if}
            <td>
              {#if r.lifecycle}
                <span class="state" class:notCommittable={!r.committable}>{r.effective}</span>
                {#if r.moves.length}
                  <span class="hint" title="Proposed in this change">from {r.base || 'no state'} → {r.moves.join(' → ')}</span>
                {/if}
                {#if !r.committable}<span class="tag" title="A change cannot commit with the node in this state">{r.created ? 'born not committable: choose another state' : 'not committable'}</span>{/if}
              {:else}
                <span class="hint">no lifecycle</span>
              {/if}
            </td>
            {#if impacts}
              <td>
                {#if r.impact}
                  <ImpactDecisions impact={r.impact} />
                  {#if r.impact.validation && !r.impact.validation.valid}<div class="invalid" role="alert" title="The review of this impact is blocked until its draft passes the validation rules">Validation failed: {r.impact.validation.message || 'the draft fails a validation rule'}</div>{/if}
                {/if}
              </td>
            {/if}
            <td class="actions">
              {#if diffable(r)}
                <button type="button" class="small ghost" title="What the change modifies of the node, against the version it started from" onclick={() => (diffing = r)}>Diff</button>
              {/if}
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
                  title="Edit the node in the node editor, which sees the graph through this change: properties, links, lifecycle"
                  onclick={() => onopennode(r, true)}
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
                    title={r.created ? 'Discard this new node' : 'Take the node out of the change, at any time: one committed already is reverted by the next commit'}
                    onclick={() => remove(r)}
                  >
                    {r.created ? 'Discard' : 'Remove from change'}
                  </button>
                {/if}
              {/if}
            </td>
          </tr>
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
      {#if !typeForm}<button type="submit" disabled={creating || busy !== ''}>{creating ? 'Creating…' : 'Create'}</button>{/if}
    </form>
    {#if typeForm}
      <div class="typeform">
        {#key newNodeType}
          <FormRenderer form={typeForm} source={formSource} busy={creating || busy !== ''} serverError={createError} onsubmit={(v) => void create(v)}
            assist={{ id: `create:${newNodeType}`, label: `${typeForm.title || 'New node'} (${newNodeType}); the key and type are chosen above the form` }} />
        {/key}
      </div>
    {:else}
      {#if createError}<div class="alert">{createError}</div>{/if}
      <p class="hint">A new node starts with its key and type only: fill in its properties with Edit.</p>
    {/if}
  {/if}

  {#if !disabled && (candidates.length || filter)}
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

{#if diffing?.impact && change?.id}
  <ImpactDiffDialog changeId={change.id} impact={diffing.impact} nodeKey={diffing.node.key ?? ''} nodeType={diffing.node.type ?? ''} flow={scope} onclose={() => (diffing = undefined)} />
{/if}

<style>
  .invalid {
    color: var(--danger, #c0392b);
    font-size: 0.85rem;
  }
  .bulk {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.4rem 0;
  }
  .pick {
    width: 1.5rem;
  }
  tr.selected td {
    background: var(--accent-soft);
  }
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
  .state.notCommittable {
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
  .typeform {
    max-width: 36rem;
    margin-top: 0.4rem;
    padding: 0.5rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
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
</style>
