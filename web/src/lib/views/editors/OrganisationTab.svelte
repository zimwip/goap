<script lang="ts">
  // Organisational unit tab. An organisation is an OrgUnit node of the organisation namespace. Its MCP
  // pane says which MCPs the unit can use: an MCP is implemented for the unit by an Adapter node
  // (`ADP:<unit>/<mcp>`, owned by the unit), the unit's INSTANCE of an adapter of the library (an algorithm of
  // type `adapter` in a published domain): it names the algorithm and gives the parameter values (root
  // directory, secret references...). A unit inherits the adapters of its ancestors; the nearest wins.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import AlgorithmParamValues from '../../components/AlgorithmParamValues.svelte';
  import { mcp, errorMessage, type Adapter, type EffectiveMcp, type Struct } from '../../api';
  import { tools, refreshTools, type LibraryAdapter } from '../../stores/tools.svelte';
  import { defaultToText, type ParamForm } from '../../algorithmForm';
  import { headGraph, findNode, applyOnMain, createNodeItem, updateNodeItem, deleteNodeItem, linkItem, refOf, type HeadGraph } from '../../graphEdit';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';

  let { tab }: { tab: Tab } = $props();

  const NS = 'organisation';
  const key = $derived(tab.params.key ?? '');

  let head = $state<HeadGraph>();
  let chain = $state<string[]>([]);
  let effective = $state<EffectiveMcp[]>([]);
  let loading = $state(false);
  let error = $state('');
  let pane = $state('overview');

  const unit = $derived(head ? findNode(head, NS, 'OrgUnit', key) : undefined);
  const nodeById = $derived(new Map((head?.nodes ?? []).map((n) => [n.id ?? '', n])));
  const parentKey = $derived.by(() => {
    const l = head?.links.find((x) => x.type === 'part_of' && x.from?.id === unit?.id);
    return l?.to?.id ? (nodeById.get(l.to.id)?.key ?? '') : '';
  });
  const childKeys = $derived(
    (head?.links ?? [])
      .filter((l) => l.type === 'part_of' && l.to?.id === unit?.id)
      .map((l) => nodeById.get(l.from?.id ?? '')?.key ?? '')
      .filter(Boolean)
      .sort(),
  );
  /** the Adapter nodes owned by this unit, by MCP name */
  const ownNodes = $derived.by(() => {
    const m = new Map<string, string>();
    for (const l of head?.links ?? []) {
      if (l.type !== 'owner' || l.to?.id !== unit?.id) continue;
      const a = nodeById.get(l.from?.id ?? '');
      if (a?.type === 'Adapter' && a.namespace === NS) m.set(String(a.props?.['mcp'] ?? ''), a.key ?? '');
    }
    return m;
  });

  async function load() {
    loading = true;
    try {
      if (!tools.loaded) await refreshTools();
      const [h, e] = await Promise.all([headGraph(), mcp.listEffective(key)]);
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

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: load }],
  );

  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview' },
    { id: 'mcp', label: 'MCP', badge: effective.length || undefined },
  ]);

  // ---- adapter instance form -------------------------------------------------------------------------

  let editing = $state(false);
  let fMcp = $state('');
  /** "<domain>/<algorithm>" of the library */
  let fLib = $state('');
  let fVersion = $state('');
  let fValues = $state<Record<string, unknown>>({});
  let fError = $state('');
  let fWarnings = $state<string[]>([]);
  let checked = $state(false);
  let saving = $state(false);

  const libKey = (l: LibraryAdapter) => `${l.domain}/${l.name}`;
  const libFor = $derived(tools.library.filter((l) => l.mcp === fMcp));
  const chosen = $derived<LibraryAdapter | undefined>(tools.library.find((l) => libKey(l) === fLib));
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
    fMcp = mcpName;
    const own = tools.library.filter((l) => l.mcp === mcpName);
    const known = from?.algorithm ? own.find((l) => l.domain === from.domain && l.name === from.algorithm) : undefined;
    fLib = known ? libKey(known) : own[0] ? libKey(own[0]) : '';
    fVersion = known ? (from?.version ?? '') : '';
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
      fError = 'Pick an adapter of the library.';
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
    return { unit: key, mcp: fMcp, domain: chosen.domain, version: fVersion.trim(), algorithm: chosen.name, params };
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
      const h = await headGraph();
      const akey = `ADP:${key}/${a.mcp}`;
      const existing = findNode(h, NS, 'Adapter', akey);
      const props: Struct = { mcp: a.mcp ?? '', domain: a.domain ?? '', version: a.version ?? '', algorithm: a.algorithm ?? '', params: a.params ?? {} };
      const u = findNode(h, NS, 'OrgUnit', key);
      if (!u) throw new Error(`unit ${key} not found`);
      const id = crypto.randomUUID();
      await applyOnMain(NS, `Adapter ${a.mcp} of ${key}`, `${existing ? 'Update' : 'Create'} the adapter of ${a.mcp} for ${key}`, h.baselineId, existing ? [updateNodeItem(existing, props)] : [createNodeItem(id, akey, 'Adapter', props), linkItem(id, 'owner', refOf(u))]);
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
    if (!confirm(`Detach ${m} from ${key}? The unit falls back on its ancestors' adapter, if any.`)) return;
    try {
      const h = await headGraph();
      const existing = findNode(h, NS, 'Adapter', `ADP:${key}/${m}`);
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
                {#if parentKey}
                  <button type="button" class="link mono" onclick={() => openTab({ kind: 'unit', params: { key: parentKey } })}>{parentKey}</button>
                {:else}<span class="muted">none (root)</span>{/if}
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
        {:else}
          <section class="card">
            <h3>MCPs available to {key}</h3>
            <table class="tbl">
              <thead><tr><th>MCP</th><th>Adapter</th><th>Connector</th><th>Defined in</th><th></th><th></th></tr></thead>
              <tbody>
                {#each effective as e (e.mcp?.name)}
                  <tr>
                    <td><code>{e.mcp?.name}</code></td>
                    <td><code>{e.adapter?.domain}/{e.adapter?.algorithm}</code>{#if e.adapter?.version}<span class="hint"> @{e.adapter.version}</span>{/if}</td>
                    <td>{e.connector || '?'}{#if e.connector && !connectorLive(e.connector)}<span class="tag warn" title={connectorKnown(e.connector) ? 'registration expired' : 'not registered'}> {connectorKnown(e.connector) ? 'expired' : 'not registered'}</span>{/if}</td>
                    <td><code>{e.adapter?.unit}</code></td>
                    <td><span class="badge">{e.inherited ? 'inherited' : 'own'}</span></td>
                    <td class="acts">
                      <button type="button" class="small" onclick={() => edit(e.mcp?.name ?? '', e.adapter)}>{e.inherited ? 'Override' : 'Edit'}</button>
                      {#if !e.inherited}<button type="button" class="small danger" onclick={() => detach(e.mcp?.name ?? '')}>Detach</button>{/if}
                    </td>
                  </tr>
                {:else}
                  <tr><td colspan="6" class="empty">No MCP is implemented for this unit or its ancestors.</td></tr>
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

          {#if editing}
            <section class="card">
              <h3>Adapter of <code>{fMcp}</code> for <code>{key}</code></h3>
              <p class="hint">
                The adapter is code of the library; the unit gives it its parameter values. The same adapter can serve several units with different values (for example another root directory).
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
                    {#if !libFor.length}<option value="">no adapter for this MCP in the library</option>{/if}
                  </select>
                </div>
                <div class="field">
                  <label for="ad-ver">Version <span class="opt">(empty: latest published{chosen?.version ? `, now ${chosen.version}` : ''})</span></label>
                  <input id="ad-ver" type="text" class="mono" bind:value={fVersion} placeholder="latest" />
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
</style>
