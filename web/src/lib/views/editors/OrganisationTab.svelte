<script lang="ts">
  // Organisational unit tab. An organisation is an OrgUnit node of the organisation namespace. Its MCP
  // pane says which MCPs the unit can use: an MCP is implemented for the unit by an Adapter node
  // (`ADP:<unit>/<mcp>`, owned by the unit), the unit's INSTANCE of an adapter definition (an
  // `AdapterDef` node of the platform namespace): it names the definition and gives the parameter values (root
  // directory, secret references...). A unit inherits the adapters of its ancestors; the nearest wins.
  // The same node can restrict the MCP for the unit and its sub-units (disabled, deny, readOnly, tools:
  // ADR 0028), with or without an adapter of its own: restrictions add up along the chain, so a unit
  // narrows what it inherits (the built-in MCPs every unit gets from the default organisation) and never
  // widens it.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import AlgorithmParamValues from '../../components/AlgorithmParamValues.svelte';
  import AssignmentsPane from '../../components/AssignmentsPane.svelte';
  import { mcp, errorMessage, type Adapter, type EffectiveMcp, type Struct } from '../../api';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import type { AdapterDef } from '../../adapterDef';
  import { defaultToText, type ParamForm } from '../../algorithmForm';
  import { headGraph, findNode, applyOnMain, createNodeItem, updateNodeItem, deleteNodeItem, moveNodeItem, currentLink, refOf, type HeadGraph } from '../../graphEdit';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';
  import { ADAPTER_TYPE, ORG_UNIT_TYPE, OWNER, PART_OF, DEFAULT_ORG, WAITING_UNIT_PROP, newUserUnit } from '../../orgTypes';
  import { hasAnyRole } from '../../stores/session.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';

  let { tab }: { tab: Tab } = $props();

  const NS = 'organisation';
  const key = $derived(tab.params.key ?? '');

  let head = $state<HeadGraph>();
  let chain = $state<string[]>([]);
  let effective = $state<EffectiveMcp[]>([]);
  let loading = $state(false);
  let error = $state('');
  let pane = $state(tab.params.pane === 'assignments' ? 'assignments' : 'overview');

  const unit = $derived(head ? findNode(head, NS, ORG_UNIT_TYPE, key) : undefined);
  const nodeById = $derived(new Map((head?.nodes ?? []).map((n) => [n.id ?? '', n])));
  const parentLink = $derived(head && unit ? currentLink(head, unit, PART_OF) : undefined);
  const parentKey = $derived(parentLink?.to?.id ? (nodeById.get(parentLink.to.id)?.key ?? '') : '');
  const orgUnits = $derived((head?.nodes ?? []).filter((n) => n.type === ORG_UNIT_TYPE && n.key !== key));
  const childKeys = $derived(
    (head?.links ?? [])
      .filter((l) => l.type === PART_OF && l.to?.id === unit?.id)
      .map((l) => nodeById.get(l.from?.id ?? '')?.key ?? '')
      .filter(Boolean)
      .sort(),
  );
  /** the Adapter nodes owned by this unit, by MCP name */
  const ownNodes = $derived.by(() => {
    const m = new Map<string, string>();
    for (const l of head?.links ?? []) {
      if (l.type !== OWNER || l.to?.id !== unit?.id) continue;
      const a = nodeById.get(l.from?.id ?? '');
      if (a?.type === ADAPTER_TYPE && a.namespace === NS) m.set(String(a.props?.['mcp'] ?? ''), a.key ?? '');
    }
    return m;
  });

  async function load() {
    loading = true;
    try {
      if (!tools.loaded) await refreshTools();
      const [h, e] = await Promise.all([headGraph(NS), mcp.listEffective(key)]);
      head = h;
      chain = e.chain ?? [];
      effective = e.mcps ?? [];
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void key;
    void load();
  });

  let moving = $state(false);
  let fParent = $state('');
  let movingBusy = $state(false);

  function startMove() {
    fParent = parentKey;
    moving = true;
  }

  async function move() {
    if (!unit || !head || !fParent || fParent === parentKey) {
      moving = false;
      return;
    }
    const target = head.nodes.find((n) => n.type === ORG_UNIT_TYPE && n.key === fParent);
    if (!target) return;
    movingBusy = true;
    error = '';
    try {
      await applyOnMain(NS, `Move ${key}`, `Move ${key} under ${fParent}`, head.baselineId, [moveNodeItem(unit, PART_OF, parentLink, refOf(target))]);
      notify(`${key} moved under ${fParent}.`, 'ok');
      moving = false;
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      movingBusy = false;
    }
  }

  // The waiting unit (ADR 0042): a unit an administrator flags, at their discretion, for users signing in
  // for the first time; with none, they join ORG-DEFAULT. Making this unit the waiting unit moves the flag in
  // one change (set here, cleared on every unit carrying it); clearing it sends newcomers back to ORG-DEFAULT.
  const flagged = $derived((head?.nodes ?? []).filter((n) => n.type === ORG_UNIT_TYPE && n.props?.[WAITING_UNIT_PROP] === true));
  const joinKey = $derived(newUserUnit(flagged));
  const isWaiting = $derived(flagged.some((n) => n.key === key));
  const isAdmin = $derived(hasAnyRole('admin'));
  let waitingBusy = $state(false);

  async function setWaiting(on: boolean) {
    if (!unit || !head || on === isWaiting) return;
    const ok = await confirmDialog(
      on
        ? {
            title: 'Waiting unit',
            message: `Users signing in for the first time will join ${key} (instead of ${joinKey}) until an administrator moves them. Existing users stay where they are.`,
            confirmLabel: 'Make waiting unit',
          }
        : { title: 'Waiting unit', message: `New users will join ${DEFAULT_ORG} again instead of ${key}.`, confirmLabel: 'Clear' },
    );
    if (!ok) return;
    waitingBusy = true;
    error = '';
    try {
      const edits = on
        ? [updateNodeItem(unit, { [WAITING_UNIT_PROP]: true }), ...flagged.filter((n) => n.id !== unit.id).map((n) => updateNodeItem(n, { [WAITING_UNIT_PROP]: null }))]
        : flagged.map((n) => updateNodeItem(n, { [WAITING_UNIT_PROP]: null }));
      await applyOnMain(NS, `Waiting unit ${on ? key : 'cleared'}`, on ? `New users wait in ${key}` : `New users join ${DEFAULT_ORG}`, head.baselineId, edits);
      notify(on ? `New users now wait in ${key}.` : `New users now join ${DEFAULT_ORG}.`, 'ok');
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      waitingBusy = false;
    }
  }

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: load }],
  );

  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview' },
    { id: 'mcp', label: 'MCP', badge: effective.length || undefined },
    { id: 'assignments', label: 'Assignments' },
  ]);

  // ---- adapter instance form -------------------------------------------------------------------------

  let editing = $state(false);
  let fMcp = $state('');
  /** name of the adapter definition */
  let fLib = $state('');
  let fValues = $state<Record<string, unknown>>({});
  let fError = $state('');
  let fWarnings = $state<string[]>([]);
  let checked = $state(false);
  let saving = $state(false);

  const libKey = (l: AdapterDef) => l.name;
  const libFor = $derived(tools.adapterDefs.filter((l) => l.mcp === fMcp));
  const chosen = $derived<AdapterDef | undefined>(tools.adapterDefs.find((l) => libKey(l) === fLib));
  const connectorLive = (id: string) => tools.connectors.find((c) => c.info?.id === id)?.live === true;
  const connectorKnown = (id: string) => tools.connectors.some((c) => c.info?.id === id);
  const paramForms = $derived<ParamForm[]>(
    (chosen?.params ?? []).map((p) => ({
      name: p.name ?? '',
      type: p.type ?? 'string',
      description: p.description ?? '',
      required: !!p.required,
      defaultValue: defaultToText(p.type ?? 'string', p.defaultValue),
      values: (p.values ?? []).join(', '),
    })),
  );

  /** opens the form for an MCP, prefilled from an adapter (its own, or the inherited one to override) */
  function edit(mcpName: string, from?: Adapter) {
    restricting = undefined;
    fMcp = mcpName;
    const own = tools.adapterDefs.filter((l) => l.mcp === mcpName);
    const known = from?.adapter ? own.find((l) => l.name === from.adapter) : undefined;
    fLib = known ? libKey(known) : own[0] ? libKey(own[0]) : '';
    fValues = known ? { ...((from?.params ?? {}) as Record<string, unknown>) } : {};
    fError = '';
    fWarnings = [];
    checked = false;
    editing = true;
    pane = 'mcp';
  }

  function build(): Adapter | undefined {
    fError = '';
    if (!fMcp) {
      fError = 'Pick an MCP.';
      return undefined;
    }
    if (!chosen) {
      fError = 'Pick an adapter.';
      return undefined;
    }
    const params: Struct = {};
    for (const p of chosen.params) {
      const v = fValues[p.name ?? ''];
      if (v === undefined || v === '') {
        if (p.required && p.defaultValue === undefined) {
          fError = `Parameter ${p.name} is required.`;
          return undefined;
        }
        continue;
      }
      params[p.name ?? ''] = v as Struct[string];
    }
    return { unit: key, mcp: fMcp, adapter: chosen.name, params };
  }

  async function check() {
    const a = build();
    if (!a) return;
    try {
      fWarnings = (await mcp.checkAdapter(a)).warnings ?? [];
      checked = true;
      if (!fWarnings.length) notify('Adapter is consistent.', 'ok');
    } catch (e) {
      fError = errorMessage(e);
    }
  }

  async function save() {
    const a = build();
    if (!a || !unit) return;
    saving = true;
    try {
      // blocking problems (unknown MCP or algorithm, parameters that do not fit) come back as errors
      fWarnings = (await mcp.checkAdapter(a)).warnings ?? [];
      const h = await headGraph(NS);
      const akey = `ADP:${key}/${a.mcp}`;
      const existing = findNode(h, NS, ADAPTER_TYPE, akey);
      const props: Struct = { mcp: a.mcp ?? '', adapter: a.adapter ?? '', params: a.params ?? {} };
      const u = findNode(h, NS, ORG_UNIT_TYPE, key);
      if (!u) throw new Error(`unit ${key} not found`);
      await applyOnMain(NS, `Adapter ${a.mcp} of ${key}`, `${existing ? 'Update' : 'Create'} the adapter of ${a.mcp} for ${key}`, h.baselineId, existing ? [updateNodeItem(existing, props)] : [createNodeItem(akey, ADAPTER_TYPE, props, [{ type: OWNER, to: refOf(u) }])]);
      notify(`Adapter ${a.mcp} saved for ${key}.`, 'ok');
      editing = false;
      await load();
    } catch (e) {
      fError = errorMessage(e);
    } finally {
      saving = false;
    }
  }

  async function detach(m: string) {
    if (!(await confirmDialog({ message: `Detach ${m} from ${key}? The unit falls back on its ancestors' adapter, if any.`, danger: true }))) return;
    try {
      const h = await headGraph(NS);
      const existing = findNode(h, NS, ADAPTER_TYPE, `ADP:${key}/${m}`);
      if (!existing) throw new Error('adapter node not found');
      await applyOnMain(NS, `Detach ${m} from ${key}`, `Delete the adapter of ${m} for ${key}`, h.baselineId, [deleteNodeItem(existing)]);
      notify(`${m} detached from ${key}.`, 'ok');
      await load();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  const attachable = $derived(tools.mcps.filter((m) => !ownNodes.has(m.name ?? '')));
  let pick = $state('');

  // ---- restriction form (ADR 0028) ----------------------------------------------------------------------

  let restricting = $state<EffectiveMcp>();
  let rDisabled = $state(false);
  let rReadOnly = $state(false);
  /** the tools the unit refuses */
  let rDeny = $state<string[]>([]);
  /** allow-list of the unit's own node, kept as it is (edited through the node) */
  let rAllow = $state<string[]>([]);
  let rError = $state('');

  /** the unit's own Adapter node of an MCP, if any */
  function ownNode(h: HeadGraph, m: string) {
    return findNode(h, NS, ADAPTER_TYPE, `ADP:${key}/${m}`);
  }

  function restrict(e: EffectiveMcp) {
    const m = e.mcp?.name ?? '';
    const own = head ? ownNode(head, m) : undefined;
    const props = (own?.props ?? {}) as Record<string, unknown>;
    rDisabled = props['disabled'] === true;
    rReadOnly = props['readOnly'] === true;
    rDeny = Array.isArray(props['deny']) ? (props['deny'] as string[]) : [];
    rAllow = Array.isArray(props['tools']) ? (props['tools'] as string[]) : [];
    rError = '';
    restricting = e;
    editing = false;
  }

  function toggleDeny(t: string, allowed: boolean) {
    rDeny = allowed ? rDeny.filter((x) => x !== t) : [...rDeny, t];
  }

  async function saveRestriction() {
    const m = restricting?.mcp?.name ?? '';
    if (!m || !unit) return;
    saving = true;
    rError = '';
    try {
      const restricts = rDisabled || rReadOnly || rDeny.length > 0 || rAllow.length > 0;
      const h = await headGraph(NS);
      const existing = ownNode(h, m);
      const u = findNode(h, NS, ORG_UNIT_TYPE, key);
      if (!u) throw new Error(`unit ${key} not found`);
      // a null value clears a property of the node
      const props: Struct = {
        mcp: m,
        disabled: rDisabled || null,
        readOnly: rReadOnly || null,
        deny: rDeny.length ? rDeny : null,
        tools: rAllow.length ? rAllow : null,
      };
      if (restricts) await mcp.checkAdapter({ unit: key, mcp: m, adapter: String(existing?.props?.['adapter'] ?? ''), disabled: rDisabled, readOnly: rReadOnly, deny: rDeny, tools: rAllow });
      const title = `Restrictions of ${m} for ${key}`;
      if (existing && !restricts && !existing.props?.['adapter']) {
        // a restriction-only node with nothing left to restrict
        await applyOnMain(NS, title, `Lift the restrictions of ${m} for ${key}`, h.baselineId, [deleteNodeItem(existing)]);
      } else if (existing) {
        await applyOnMain(NS, title, `Restrict ${m} for ${key}`, h.baselineId, [updateNodeItem(existing, props)]);
      } else if (restricts) {
        await applyOnMain(NS, title, `Restrict ${m} for ${key}`, h.baselineId, [createNodeItem(`ADP:${key}/${m}`, ADAPTER_TYPE, props, [{ type: OWNER, to: refOf(u) }])]);
      }
      notify(`Restrictions of ${m} saved for ${key}.`, 'ok');
      restricting = undefined;
      await load();
    } catch (e) {
      rError = errorMessage(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="editor-page">
  {#if error}<div class="alert">{error}</div>{/if}
  {#if !unit && !loading}
    <p class="empty">Unit {key} is not on the graph.</p>
  {:else if unit}
    <EditorPanes {panes} bind:active={pane} label="Organisation sections">
      {#snippet children(active)}
        {#if active === 'overview'}
          <section class="card">
            <div class="head"><Icon name="user" size={18} /><h2>{String(unit.props?.['name'] ?? key)}</h2></div>
            <dl class="kv">
              <dt>Key</dt><dd><code>{key}</code></dd>
              {#if unit.props?.['kind']}<dt>Kind</dt><dd>{unit.props['kind']}</dd>{/if}
              {#if unit.props?.['description']}<dt>Description</dt><dd>{unit.props['description']}</dd>{/if}
              <dt>Part of</dt>
              <dd>
                {#if !moving}
                  {#if parentKey}
                    <button type="button" class="link mono" onclick={() => openTab({ kind: 'unit', params: { key: parentKey } })}>{parentKey}</button>
                  {:else}<span class="muted">none (root)</span>{/if}
                  {#if key !== DEFAULT_ORG}
                    <button type="button" class="small ghost" onclick={startMove}>Move to…</button>
                  {/if}
                {:else}
                  <select bind:value={fParent} disabled={movingBusy}>
                    {#each orgUnits as o (o.id)}
                      <option value={o.key}>{String(o.props?.['name'] ?? o.key)}</option>
                    {/each}
                  </select>
                  <button type="button" class="small primary" disabled={movingBusy} onclick={move}>Move</button>
                  <button type="button" class="small" disabled={movingBusy} onclick={() => (moving = false)}>Cancel</button>
                {/if}
              </dd>
              <dt>New users</dt>
              <dd>
                {#if isWaiting}
                  <span class="badge">waiting unit: new users join it</span>
                  {#if isAdmin}<button type="button" class="small ghost" disabled={waitingBusy} onclick={() => setWaiting(false)}>Clear</button>{/if}
                {:else}
                  <span class="muted">join <button type="button" class="link mono" onclick={() => openTab({ kind: 'unit', params: { key: joinKey } })}>{joinKey}</button></span>
                  {#if isAdmin}<button type="button" class="small ghost" disabled={waitingBusy} onclick={() => setWaiting(true)}>Make waiting unit</button>{/if}
                {/if}
              </dd>
              {#if childKeys.length}
                <dt>Sub-units</dt>
                <dd>
                  {#each childKeys as c (c)}<button type="button" class="link mono" onclick={() => openTab({ kind: 'unit', params: { key: c } })}>{c}</button>{' '}{/each}
                </dd>
              {/if}
              <dt>Adapters resolved along</dt>
              <dd><code>{chain.join(' → ')}</code></dd>
            </dl>
            <p class="hint">
              The unit holds the changes that name it. The MCPs its actions can use are the ones an adapter implements for it or, failing that, for the nearest ancestor (the default organisation is the root of every unit).
            </p>
          </section>
        {:else if active === 'mcp'}
          <section class="card">
            <h3>MCPs available to {key}</h3>
            <table class="tbl">
              <thead><tr><th>MCP</th><th>Adapter</th><th>Connector</th><th>Defined in</th><th>Tools</th><th></th><th></th></tr></thead>
              <tbody>
                {#each effective as e (e.mcp?.name)}
                  <tr>
                    <td><code>{e.mcp?.name}</code>{#if e.builtin}<span class="tag"> built in</span>{/if}</td>
                    <td><code>{e.adapter?.adapter}</code></td>
                    <td>{e.connector || '?'}{#if e.connector && !connectorLive(e.connector)}<span class="tag warn" title={connectorKnown(e.connector) ? 'registration expired' : 'not registered'}> {connectorKnown(e.connector) ? 'expired' : 'not registered'}</span>{/if}</td>
                    <td><code>{e.adapter?.unit}</code></td>
                    <td title={(e.allowedTools ?? []).join(', ')}>
                      {#if e.disabled}<span class="tag warn">disabled</span>
                      {:else}{e.allowedTools?.length ?? 0}/{e.mcp?.tools?.length ?? 0}{/if}
                      {#if e.restrictedBy?.length}<span class="tag"> restricted by {e.restrictedBy.join(', ')}</span>{/if}
                    </td>
                    <td><span class="badge">{e.inherited ? 'inherited' : 'own'}</span></td>
                    <td class="acts">
                      <button type="button" class="small" onclick={() => restrict(e)}>Restrict</button>
                      {#if !e.builtin}<button type="button" class="small" onclick={() => edit(e.mcp?.name ?? '', e.adapter)}>{e.inherited ? 'Override' : 'Edit'}</button>{/if}
                      {#if !e.inherited}<button type="button" class="small danger" onclick={() => detach(e.mcp?.name ?? '')}>Detach</button>{/if}
                    </td>
                  </tr>
                {:else}
                  <tr><td colspan="7" class="empty">No MCP is implemented for this unit or its ancestors.</td></tr>
                {/each}
              </tbody>
            </table>
            <div class="row attach">
              <select bind:value={pick} aria-label="MCP to attach">
                <option value="">Attach an MCP…</option>
                {#each attachable as m (m.name)}<option value={m.name}>{m.name}</option>{/each}
              </select>
              <button
                type="button"
                class="small"
                disabled={!pick}
                onclick={() => {
                  edit(pick, effective.find((x) => x.mcp?.name === pick)?.adapter);
                  pick = '';
                }}>Attach</button
              >
            </div>
          </section>

          {#if restricting}
            {@const rm = restricting.mcp}
            <section class="card">
              <h3>Restrictions of <code>{rm?.name}</code> for <code>{key}</code></h3>
              <p class="hint">
                They apply to {key} and its sub-units, on top of the restrictions of its ancestors{restricting.restrictedBy?.filter((u) => u !== key).length
                  ? ` (${restricting.restrictedBy.filter((u) => u !== key).join(', ')})`
                  : ''}: a unit narrows what it inherits, it cannot widen it. The implementation stays the one resolved for the unit.
              </p>
              {#if rError}<div class="alert">{rError}</div>{/if}
              <label class="check"><input type="checkbox" bind:checked={rDisabled} /> Disable the MCP</label>
              <label class="check"><input type="checkbox" bind:checked={rReadOnly} disabled={rDisabled} /> Read-only tools only</label>
              <h4>Tools</h4>
              <ul class="tools">
                {#each rm?.tools ?? [] as t (t.name)}
                  <li>
                    <label class="check">
                      <input
                        type="checkbox"
                        checked={!rDeny.includes(t.name ?? '')}
                        disabled={rDisabled || (rReadOnly && !t.readOnly)}
                        onchange={(ev) => toggleDeny(t.name ?? '', (ev.currentTarget as HTMLInputElement).checked)}
                      />
                      <code>{t.name}</code>{#if t.readOnly}<span class="tag"> read-only</span>{/if}
                      {#if t.description}<span class="muted"> {t.description}</span>{/if}
                    </label>
                  </li>
                {/each}
              </ul>
              {#if rAllow.length}<p class="hint">The node also allows only: <code>{rAllow.join(', ')}</code>.</p>{/if}
              <div class="row">
                <button type="button" class="small primary" disabled={saving} onclick={saveRestriction}>Save</button>
                <button type="button" class="small" onclick={() => (restricting = undefined)}>Cancel</button>
              </div>
            </section>
          {/if}

          {#if editing}
            <section class="card">
              <h3>Adapter of <code>{fMcp}</code> for <code>{key}</code></h3>
              <p class="hint">
                The adapter is defined once on the platform (Adapters section); the unit gives it its parameter values. The same adapter can serve several units with different values (for example another root directory).
              </p>
              {#if fError}<div class="alert">{fError}</div>{/if}
              <div class="grid">
                <div class="field">
                  <label for="ad-mcp">MCP</label>
                  <select
                    id="ad-mcp"
                    bind:value={fMcp}
                    disabled={ownNodes.has(fMcp)}
                    onchange={() => {
                      fLib = libFor[0] ? libKey(libFor[0]) : '';
                      fValues = {};
                    }}
                  >
                    {#each tools.mcps as m (m.name)}<option value={m.name}>{m.name}</option>{/each}
                  </select>
                </div>
                <div class="field">
                  <label for="ad-lib">Adapter</label>
                  <select id="ad-lib" bind:value={fLib} onchange={() => (fValues = {})}>
                    {#each libFor as l (libKey(l))}
                      <option value={libKey(l)}>{libKey(l)} · connector {l.connector}{connectorLive(l.connector) ? '' : connectorKnown(l.connector) ? ' (expired)' : ' (not registered)'}</option>
                    {/each}
                    {#if !libFor.length}<option value="">no adapter defined for this MCP</option>{/if}
                  </select>
                </div>
              </div>
              {#if chosen}
                {#if chosen.description}<p class="hint">{chosen.description}</p>{/if}
                {#if !connectorLive(chosen.connector)}
                  <div class="alert warn">Connector <code>{chosen.connector}</code> is {connectorKnown(chosen.connector) ? 'registered but its registration expired' : 'not registered'}: tool calls will fail until it registers.</div>
                {/if}
                <h4>Parameters</h4>
                <AlgorithmParamValues params={paramForms} bind:values={fValues} idPrefix="ad-par" />
                {#if paramForms.some((p) => p.type === 'secret')}
                  <p class="hint">A secret is a reference resolved by the hub at call time: <code>&lt;vault path&gt;#&lt;field&gt;</code> or <code>env:&lt;VAR&gt;</code>. The value itself is never stored on the graph.</p>
                {/if}
              {/if}

              {#if fWarnings.length}
                <div class="alert warn">
                  <ul>{#each fWarnings as w (w)}<li>{w}</li>{/each}</ul>
                </div>
              {:else if checked}
                <p class="hint">No problem found.</p>
              {/if}
              <div class="row">
                <button type="button" class="small" disabled={!chosen} onclick={check}>Check</button>
                <button type="button" class="small primary" disabled={saving || !chosen} onclick={save}>Save</button>
                <button type="button" class="small" onclick={() => (editing = false)}>Cancel</button>
              </div>
            </section>
          {/if}
        {:else if active === 'assignments' && head}
          <AssignmentsPane {head} fixedOrg={key} autoOpen={tab.params.newAssignment === '1'} onChanged={load} />
        {/if}
      {/snippet}
    </EditorPanes>
  {/if}
</div>

<style>
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.6rem;
  }
  .head h2 {
    margin: 0;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.25rem 1rem;
  }
  .kv dt {
    color: var(--muted);
  }
  .kv dd {
    margin: 0;
  }
  .tbl {
    width: 100%;
    border-collapse: collapse;
  }
  .tbl th,
  .tbl td {
    text-align: left;
    padding: 0.25rem 0.5rem;
    border-bottom: 1px solid var(--border, #8884);
  }
  .acts {
    white-space: nowrap;
    text-align: right;
  }
  .attach {
    margin-top: 0.6rem;
    align-items: center;
  }
  .tag.warn {
    color: var(--warn, #b80);
    font-size: 0.8rem;
  }
  h4 {
    margin: 0.9rem 0 0.3rem;
  }
  .tools {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .check {
    display: block;
    margin: 0.2rem 0;
  }
</style>
