<script lang="ts">
  // Organisational unit tab. An organisation is an OrgUnit node of the organisation namespace. Its MCP
  // pane says which MCPs the unit can use: an MCP is implemented for the unit by an Adapter node
  // (`ADP:<unit>/<mcp>`, owned by the unit) that picks a connector, gives it its parameters and secrets and
  // maps the tools of the MCP onto its operations. A unit inherits the adapters of its ancestors; the nearest wins.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import { mcp, errorMessage, type Adapter, type EffectiveMcp, type Mcp, type Struct, type ToolMapping } from '../../api';
  import { tools, refreshTools } from '../../stores/tools.svelte';
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

  // ---- adapter form ---------------------------------------------------------------------------------

  interface Field {
    name: string;
    type: string;
    required: boolean;
    description: string;
  }
  interface Mapping {
    tool: string;
    operation: string;
    args: string;
    resultPath: string;
  }

  let editing = $state(false);
  let fMcp = $state('');
  let fConnector = $state('');
  let fValues = $state<Record<string, string>>({});
  let fExtra = $state('');
  let fSecrets = $state<Record<string, string>>({});
  let fMaps = $state<Mapping[]>([]);
  let fError = $state('');
  let fWarnings = $state<string[]>([]);
  let checked = $state(false);
  let saving = $state(false);

  const mcpDef = $derived<Mcp | undefined>(tools.mcps.find((m) => m.name === fMcp));
  const conn = $derived(tools.connectors.find((c) => c.info?.id === fConnector)?.info);
  const schema = $derived((conn?.configSchema ?? {}) as { properties?: Record<string, Record<string, unknown>>; required?: string[] });
  const SIMPLE = ['string', 'number', 'integer', 'boolean'];
  const fields = $derived<Field[]>(
    Object.entries(schema.properties ?? {})
      .filter(([, d]) => SIMPLE.includes(String(d['type'])))
      .map(([name, d]) => ({ name, type: String(d['type']), required: (schema.required ?? []).includes(name), description: String(d['description'] ?? '') })),
  );
  const simpleNames = $derived(new Set(fields.map((f) => f.name)));
  const opNames = $derived((conn?.operations ?? []).map((o) => o.name ?? ''));

  function argsTemplate(t: { inputSchema?: Struct }): string {
    const props = Object.keys(((t.inputSchema ?? {}) as { properties?: Record<string, unknown> }).properties ?? {});
    return props.length ? JSON.stringify(Object.fromEntries(props.map((p) => [p, `$.${p}`])), null, 2) : '';
  }

  function mappingsFor(m: Mcp | undefined, from: ToolMapping[] = []): Mapping[] {
    return (m?.tools ?? []).map((t) => {
      const cur = from.find((x) => x.tool === t.name);
      return {
        tool: t.name ?? '',
        operation: cur?.operation ?? '',
        args: cur ? (cur.arguments && Object.keys(cur.arguments).length ? JSON.stringify(cur.arguments, null, 2) : '') : argsTemplate(t),
        resultPath: cur?.resultPath ?? '',
      };
    });
  }

  /** opens the form for an MCP, prefilled from an adapter (its own, or the inherited one to override) */
  function edit(mcpName: string, from?: Adapter) {
    fMcp = mcpName;
    fConnector = from?.connector ?? tools.connectors[0]?.info?.id ?? '';
    const cfg = { ...(from?.config ?? {}) } as Record<string, unknown>;
    const sch = ((tools.connectors.find((c) => c.info?.id === fConnector)?.info?.configSchema ?? {}) as { properties?: Record<string, Record<string, unknown>> }).properties ?? {};
    const vals: Record<string, string> = {};
    for (const [n, d] of Object.entries(sch)) {
      if (SIMPLE.includes(String(d['type'])) && cfg[n] !== undefined) {
        vals[n] = String(cfg[n]);
        delete cfg[n];
      }
    }
    fValues = vals;
    fExtra = Object.keys(cfg).length ? JSON.stringify(cfg, null, 2) : '';
    fSecrets = { ...(from?.secrets ?? {}) };
    fMaps = mappingsFor(
      tools.mcps.find((m) => m.name === mcpName),
      from?.tools,
    );
    fError = '';
    fWarnings = [];
    checked = false;
    editing = true;
    pane = 'mcp';
  }

  function build(): Adapter | undefined {
    fError = '';
    if (!fMcp || !fConnector) {
      fError = 'Pick an MCP and a connector.';
      return undefined;
    }
    const config: Struct = {};
    for (const f of fields) {
      const v = (fValues[f.name] ?? '').trim();
      if (v === '') {
        if (f.required && f.type !== 'boolean') {
          fError = `Parameter ${f.name} is required.`;
          return undefined;
        }
        continue;
      }
      if (f.type === 'boolean') config[f.name] = v === 'true';
      else if (f.type === 'string') config[f.name] = v;
      else {
        const n = Number(v);
        if (Number.isNaN(n) || (f.type === 'integer' && !Number.isInteger(n))) {
          fError = `Parameter ${f.name} must be ${f.type === 'integer' ? 'an integer' : 'a number'}.`;
          return undefined;
        }
        config[f.name] = n;
      }
    }
    if (fExtra.trim()) {
      try {
        const extra = JSON.parse(fExtra);
        if (extra === null || typeof extra !== 'object' || Array.isArray(extra)) throw new Error('an object is expected');
        for (const [k, v] of Object.entries(extra as Struct)) if (!simpleNames.has(k)) config[k] = v;
      } catch (e) {
        fError = `Other parameters: invalid JSON (${e instanceof Error ? e.message : e})`;
        return undefined;
      }
    }
    const secrets: Record<string, string> = {};
    for (const n of conn?.secretNames ?? []) if ((fSecrets[n] ?? '').trim()) secrets[n] = fSecrets[n].trim();
    const toolMaps: ToolMapping[] = [];
    for (const m of fMaps) {
      if (!m.operation) continue;
      let args: Struct | undefined;
      if (m.args.trim()) {
        try {
          args = JSON.parse(m.args) as Struct;
        } catch (e) {
          fError = `Tool ${m.tool}: arguments are not valid JSON (${e instanceof Error ? e.message : e})`;
          return undefined;
        }
      }
      toolMaps.push({ tool: m.tool, operation: m.operation, ...(args ? { arguments: args } : {}), ...(m.resultPath.trim() ? { resultPath: m.resultPath.trim() } : {}) });
    }
    return { unit: key, mcp: fMcp, connector: fConnector, config, secrets, tools: toolMaps };
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
      // blocking problems (unknown MCP or tool) come back as errors
      fWarnings = (await mcp.checkAdapter(a)).warnings ?? [];
      const h = await headGraph();
      const akey = `ADP:${key}/${a.mcp}`;
      const existing = findNode(h, NS, 'Adapter', akey);
      const props: Struct = { mcp: a.mcp ?? '', connector: a.connector ?? '', config: a.config ?? {}, secrets: a.secrets ?? {}, tools: (a.tools ?? []) as unknown as Struct[] };
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
              <thead><tr><th>MCP</th><th>Connector</th><th>Defined in</th><th></th><th></th></tr></thead>
              <tbody>
                {#each effective as e (e.mcp?.name)}
                  <tr>
                    <td><code>{e.mcp?.name}</code></td>
                    <td>{e.adapter?.connector}</td>
                    <td><code>{e.adapter?.unit}</code></td>
                    <td><span class="badge">{e.inherited ? 'inherited' : 'own'}</span></td>
                    <td class="acts">
                      <button type="button" class="small" onclick={() => edit(e.mcp?.name ?? '', e.adapter)}>{e.inherited ? 'Override' : 'Edit'}</button>
                      {#if !e.inherited}<button type="button" class="small danger" onclick={() => detach(e.mcp?.name ?? '')}>Detach</button>{/if}
                    </td>
                  </tr>
                {:else}
                  <tr><td colspan="5" class="empty">No MCP is implemented for this unit or its ancestors.</td></tr>
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
              {#if fError}<div class="alert">{fError}</div>{/if}
              <div class="grid">
                <div class="field">
                  <label for="ad-mcp">MCP</label>
                  <select id="ad-mcp" bind:value={fMcp} disabled={ownNodes.has(fMcp)} onchange={() => (fMaps = mappingsFor(mcpDef))}>
                    {#each tools.mcps as m (m.name)}<option value={m.name}>{m.name}</option>{/each}
                  </select>
                </div>
                <div class="field">
                  <label for="ad-conn">Connector</label>
                  <select id="ad-conn" bind:value={fConnector}>
                    {#each tools.connectors as c (c.info?.id)}<option value={c.info?.id}>{c.info?.id}{c.live ? '' : ' (expired)'}</option>{/each}
                    {#if fConnector && !tools.connectors.some((c) => c.info?.id === fConnector)}<option value={fConnector}>{fConnector} (not registered)</option>{/if}
                  </select>
                </div>
              </div>

              <h4>Connector parameters</h4>
              {#each fields as f (f.name)}
                <div class="field">
                  <label for="ad-cfg-{f.name}">{f.name}{#if f.required} <span class="req">*</span>{/if} <span class="opt">({f.type})</span></label>
                  {#if f.type === 'boolean'}
                    <select id="ad-cfg-{f.name}" bind:value={fValues[f.name]}>
                      <option value="">unset</option><option value="true">true</option><option value="false">false</option>
                    </select>
                  {:else}
                    <input id="ad-cfg-{f.name}" type={f.type === 'string' ? 'text' : 'number'} step={f.type === 'integer' ? 1 : 'any'} class:mono={f.type === 'string'} bind:value={fValues[f.name]} />
                  {/if}
                  {#if f.description}<span class="hint">{f.description}</span>{/if}
                </div>
              {/each}
              <div class="field">
                <label for="ad-extra">{fields.length ? 'Other parameters' : 'Parameters'} <span class="opt">(JSON object)</span></label>
                <textarea id="ad-extra" class="mono" rows="3" bind:value={fExtra} placeholder={'{}'}></textarea>
              </div>

              {#if conn?.secretNames?.length}
                <h4>Secrets</h4>
                {#each conn.secretNames as n (n)}
                  <div class="field">
                    <label for="ad-sec-{n}">{n}</label>
                    <input id="ad-sec-{n}" class="mono" type="text" bind:value={fSecrets[n]} placeholder="secret/path#field  or  env:VARIABLE" />
                  </div>
                {/each}
                <p class="hint">A reference resolved by the hub at call time: <code>&lt;vault path&gt;#&lt;field&gt;</code> or <code>env:&lt;VAR&gt;</code>. The value itself is never stored on the graph.</p>
              {/if}

              <h4>Tools of the MCP → operations of the connector</h4>
              {#each fMaps as m, i (m.tool)}
                <div class="map">
                  <div class="field">
                    <label for="ad-op-{i}"><code>{m.tool}</code></label>
                    <select id="ad-op-{i}" bind:value={m.operation}>
                      <option value="">not mapped (tool unavailable)</option>
                      {#each opNames as o (o)}<option value={o}>{o}</option>{/each}
                      {#if m.operation && !opNames.includes(m.operation)}<option value={m.operation}>{m.operation} (unknown)</option>{/if}
                    </select>
                  </div>
                  <div class="field grow">
                    <label for="ad-args-{i}">Arguments <span class="opt">("$.name" = argument of the tool call)</span></label>
                    <textarea id="ad-args-{i}" class="mono" rows="3" bind:value={m.args}></textarea>
                  </div>
                  <div class="field">
                    <label for="ad-rp-{i}">Result path</label>
                    <input id="ad-rp-{i}" class="mono" type="text" bind:value={m.resultPath} placeholder="whole result" />
                  </div>
                </div>
              {/each}

              {#if fWarnings.length}
                <div class="alert warn">
                  <ul>{#each fWarnings as w (w)}<li>{w}</li>{/each}</ul>
                </div>
              {:else if checked}
                <p class="hint">No problem found.</p>
              {/if}
              <div class="row">
                <button type="button" class="small" onclick={check}>Check</button>
                <button type="button" class="small primary" disabled={saving} onclick={save}>Save</button>
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
  .map {
    display: flex;
    gap: 0.6rem;
    align-items: flex-start;
  }
  h4 {
    margin: 0.9rem 0 0.3rem;
  }
  .req {
    color: var(--danger, #c33);
  }
</style>
