<script lang="ts">
  // "Adapter" tab: an adapter definition is a node `ADD:<name>` of the platform namespace, changed through a change
  // applied on main like every modification of the platform. It implements the tools of one MCP with the operations of
  // one connector, so the three are edited together here: the binding (MCP and connector, what each side offers), the
  // parameters an organisational unit sets, and the code. Units instantiate it from their MCP pane (ADR 0019).
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import RowTools from '../../components/RowTools.svelte';
  import CodeEditor from '../../components/CodeEditor.svelte';
  import { provideActions, useReveal, notify } from '../../shell/workbench.svelte';
  import { closeTab, openTab } from '../../shell/tabs.svelte';
  import { moveItem } from '../../methodologyForm';
  import { algorithmFromForm, algorithmToForm, emptyAlgorithm, emptyParam, ALGORITHM_LANGUAGES, PARAM_TYPES, SECRET_HINT, type AlgorithmForm } from '../../algorithmForm';
  import { mcp as hub, errorMessage } from '../../api';
  import { headGraph, findNode, applyOnMain, createNodeItem, updateNodeItem, retireNodeItem } from '../../graphEdit';
  import { ADAPTER_DEF_TYPE, adapterDefKey, adapterDefProps } from '../../adapterDef';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { ns } from '../../stores/session.svelte';
  import { ALGORITHM_TEMPLATES, algorithmUsage } from '../../dsl';
  import { confirmDialog } from '../../shell/confirmState.svelte';

  let { tab }: { tab: Tab } = $props();

  const usage = algorithmUsage('adapter');
  let root = $state<HTMLElement>();

  $effect(() => {
    if (!tools.loaded) void refreshTools();
  });

  const isNew = $derived(!tab.params.name);
  let a = $state<AlgorithmForm>(emptyAlgorithm('adapter', ''));
  let error = $state('');
  let saving = $state(false);
  let loadedKey = $state<string>();

  $effect(() => {
    const key = tab.params.name ?? '';
    if (loadedKey === key) return;
    if (key) {
      const d = tools.adapterDefs.find((x) => x.name === key);
      if (!d) return; // not loaded yet
      a = algorithmToForm({ ...d, type: 'adapter' });
    } else {
      a = emptyAlgorithm('adapter', '');
    }
    loadedKey = key;
  });

  const mcp = $derived(tools.mcps.find((m) => m.name === a.mcp));
  const connector = $derived(tools.connectors.find((c) => c.info?.id === a.connector));
  const operations = $derived(connector?.info?.operations ?? []);
  // a name counts as handled when the code quotes it
  const mentions = (n?: string) => !!n && (a.code.includes(`'${n}'`) || a.code.includes(`"${n}"`) || a.code.includes(`\`${n}\``));
  const missingTools = $derived((mcp?.tools ?? []).filter((t) => !mentions(t.name)));
  type ConfigProps = Record<string, { type?: string; description?: string }>;
  const configProps = $derived(((connector?.info?.configSchema as { properties?: ConfigProps } | undefined)?.properties ?? {}) as ConfigProps);
  const missingParams = $derived(Object.keys(configProps).filter((n) => !a.params.some((p) => p.name === n)));

  let generating = $state(false);
  let genError = $state('');
  let genNote = $state('');

  function untouched(): boolean {
    return !a.code.trim() || Object.values(ALGORITHM_TEMPLATES).some((t) => Object.values(t).includes(a.code));
  }

  /** Fills the code (one case per MCP tool, the connector operations available through ctx.call) and the connector's parameters. */
  async function generate() {
    genError = '';
    genNote = '';
    if (!a.mcp || !a.connector) {
      genError = 'Pick an MCP and a connector first.';
      return;
    }
    if (!untouched() && !(await confirmDialog('Replace the code of the adapter with a generated template?'))) return;
    generating = true;
    try {
      const t = await hub.adapterTemplate(a.mcp, a.connector);
      a.language = 'javascript';
      a.code = t.code ?? a.code;
      const have = new Set(a.params.map((p) => p.name));
      let added = 0;
      for (const p of t.params ?? []) {
        if (!p.name || have.has(p.name)) continue;
        a.params.push({ ...emptyParam(), name: p.name, type: p.type ?? 'string', description: p.description ?? '', required: !!p.required });
        added++;
      }
      genNote = `Template generated${added ? `, ${added} parameter(s) added` : ''}. Complete the mapping: rename operations, reshape arguments and results.`;
    } catch (e) {
      genError = errorMessage(e);
    } finally {
      generating = false;
    }
  }

  function addMissingParams() {
    const secrets = new Set(connector?.info?.secretNames ?? []);
    for (const n of missingParams) {
      const type = secrets.has(n) ? 'secret' : (configProps[n]?.type ?? 'string');
      a.params.push({ ...emptyParam(), name: n, type: (PARAM_TYPES as string[]).includes(type) ? type : 'string', description: configProps[n]?.description ?? '' });
    }
  }

  async function save() {
    error = '';
    const def = algorithmFromForm(a);
    if (!def.name || !def.mcp || !def.connector) {
      error = 'Name, MCP and connector are required.';
      return;
    }
    saving = true;
    try {
      const h = await headGraph(ns.platform);
      const key = adapterDefKey(def.name);
      const existing = findNode(h, ns.platform, ADAPTER_DEF_TYPE, key);
      if (isNew && existing) throw new Error(`an adapter named ${def.name} already exists`);
      const props = adapterDefProps({ name: def.name ?? '', description: def.description ?? '', mcp: def.mcp ?? '', connector: def.connector ?? '', language: def.language ?? 'javascript', code: def.code ?? '', params: def.params ?? [] });
      const item = existing ? updateNodeItem(existing, props) : createNodeItem(key, ADAPTER_DEF_TYPE, props);
      await applyOnMain(ns.platform, `Adapter ${def.name}`, `${existing ? 'Update' : 'Create'} adapter ${def.name} (${def.mcp} on ${def.connector})`, h.baselineId, [item]);
      await refreshTools();
      notify(`Adapter ${def.name} saved`, 'ok');
      if (isNew) {
        closeTab(tab.id, { force: true });
        openTab({ kind: 'adapter', params: { name: def.name ?? '' } }, { pin: true });
      }
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!(await confirmDialog({ message: `Retire the adapter ${a.name}? Units that instantiate it stop resolving their ${a.mcp} tools; it can be restored by saving it again.`, danger: true }))) return;
    try {
      const h = await headGraph(ns.platform);
      const existing = findNode(h, ns.platform, ADAPTER_DEF_TYPE, adapterDefKey(a.name));
      if (!existing) throw new Error(`adapter ${a.name} is not on the graph`);
      await applyOnMain(ns.platform, `Retire adapter ${a.name}`, `Retire adapter ${a.name}`, h.baselineId, [retireNodeItem(existing)]);
      await refreshTools();
      closeTab(tab.id, { force: true });
    } catch (e) {
      error = errorMessage(e);
    }
  }

  provideActions(
    () => tab.id,
    () => [
      { id: 'save', label: saving ? 'Saving…' : 'Save', icon: 'save', primary: true, shortcut: 'Ctrl+S', disabled: saving || !a.name.trim(), title: 'Applies a change on the platform namespace', run: save },
      ...(isNew ? [] : [{ id: 'delete', label: 'Delete', icon: 'trash' as const, danger: true, run: remove }]),
    ],
  );
  useReveal(
    () => tab.id,
    () => root,
  );
</script>

<div class="editor-page" bind:this={root}>
  {#if !isNew && loadedKey !== tab.params.name}
    <p class="empty">{tools.loaded ? `Adapter ${tab.params.name} is not on the graph.` : 'Loading…'}</p>
  {:else}
    <div class="editor-head">
      <Icon name="zap" size={18} />
      <h2>{isNew ? 'New adapter' : a.name}</h2>
      <span class="hint">adapter definition · platform</span>
    </div>
    {#if error}<div class="alert">{error}</div>{/if}
    <p class="hint">Saving applies a change on the platform namespace: it is journaled and reviewable like any modification of the platform.</p>

    <fieldset class="plain">
      <section class="card">
        <h3>General</h3>
        <div class="grid">
          <div class="field">
            <label for="adp-name">Name</label>
            <input id="adp-name" type="text" class="mono" bind:value={a.name} disabled={!isNew} placeholder="localfs-documents" />
          </div>
          <div class="field">
            <label for="adp-lang">Language</label>
            <select id="adp-lang" bind:value={a.language}>
              {#each ALGORITHM_LANGUAGES as l (l)}<option value={l}>{l}</option>{/each}
            </select>
          </div>
        </div>
        <div class="field">
          <label for="adp-desc">Description</label>
          <input id="adp-desc" type="text" bind:value={a.description} />
        </div>
        <p class="hint">
          An adapter is defined once here, on the platform. It implements the tools of one MCP with the operations of one connector; each organisational unit then
          instantiates it with its own parameter values (Organisation › MCP).
        </p>
      </section>

      <section class="card">
        <h3>MCP and connector</h3>
        <div class="binding">
          <div class="side">
            <div class="field">
              <label for="adp-mcp">MCP (what an LLM uses)</label>
              <select id="adp-mcp" bind:value={a.mcp}>
                <option value="">Pick an MCP…</option>
                {#each tools.mcps as m (m.name)}<option value={m.name}>{m.name}</option>{/each}
                {#if a.mcp && !mcp}<option value={a.mcp}>{a.mcp} (unknown)</option>{/if}
              </select>
            </div>
            <div class="links">
              {#if mcp}<button type="button" class="link small" onclick={() => openTab({ kind: 'mcp', params: { name: a.mcp } }, { pin: true })}>Edit this MCP</button>{/if}
              <button type="button" class="link small" onclick={() => openTab({ kind: 'mcp', params: { name: '' } }, { pin: true })}>New MCP</button>
            </div>
            {#if mcp}
              <ul class="plain-list">
                {#each mcp.tools ?? [] as t (t.name)}
                  <li>
                    <span class="mark" class:ok={mentions(t.name)} title={mentions(t.name) ? 'Handled in the code' : 'Not handled in the code'}>{mentions(t.name) ? '✓' : '○'}</span>
                    <code>{t.name}</code> <span class="hint">{t.description}</span>
                  </li>
                {:else}
                  <li class="hint">This MCP declares no tool.</li>
                {/each}
              </ul>
            {/if}
          </div>
          <div class="arrow" aria-hidden="true">→</div>
          <div class="side">
            <div class="field">
              <label for="adp-conn">Connector (the real service)</label>
              <select id="adp-conn" bind:value={a.connector}>
                <option value="">Pick a connector…</option>
                {#each tools.connectors as c (c.info?.id)}<option value={c.info?.id}>{c.info?.id}{c.live ? '' : ' (expired)'}</option>{/each}
                {#if a.connector && !connector}<option value={a.connector}>{a.connector} (not registered)</option>{/if}
              </select>
            </div>
            <div class="links">
              {#if connector}<button type="button" class="link small" onclick={() => openTab({ kind: 'connector', params: { id: a.connector } }, { pin: true })}>View this connector</button>{/if}
            </div>
            {#if connector}
              <ul class="plain-list">
                {#each operations as op (op.name)}
                  <li>
                    <span class="mark" class:ok={mentions(op.name)} title={mentions(op.name) ? 'Called by the code' : 'Not called by the code'}>{mentions(op.name) ? '✓' : '○'}</span>
                    <code>{op.name}</code> <span class="hint">{op.description}</span>
                  </li>
                {/each}
              </ul>
            {:else if a.connector}
              <p class="hint">The connector is not registered: its operations are unknown.</p>
            {/if}
          </div>
        </div>
        <div class="row">
            <button type="button" class="small" disabled={generating || !a.mcp || !a.connector} onclick={generate}>{generating ? 'Generating…' : 'Generate code from MCP and connector'}</button>
            <span class="hint">Needs the connector to be registered: the template follows the MCP tools and the operations it exposes.</span>
        </div>
        {#if genError}<div class="alert">{genError}</div>{/if}
        {#if genNote}<p class="hint">{genNote}</p>{/if}
        {#if mcp && missingTools.length}
          <p class="hint">Tools with no case in the code: {missingTools.map((t) => t.name).join(', ')}.</p>
        {/if}
      </section>

      <section class="card">
        <h3>Parameters</h3>
        <p class="hint">
          Typed values an instance sets and the code reads with <code>ctx.param(name)</code> (Go: <code>ctx.Param(name)</code>). They are handed to the connector as its
          configuration under the same names (see its config schema); a <code>secret</code> parameter is a reference (<code>{SECRET_HINT}</code>) the hub resolves and
          gives to the connector: the code never reads it.
        </p>
        {#each a.params as p, i}
          <div class="prow">
            <input type="text" class="mono" aria-label="Parameter name" bind:value={p.name} placeholder="rootDir" />
            <select aria-label="Parameter type" bind:value={p.type}>
              {#each PARAM_TYPES as t (t)}<option value={t}>{t}</option>{/each}
            </select>
            <label class="check"><input type="checkbox" bind:checked={p.required} /> required</label>
            {#if p.type !== 'secret'}
              <input type="text" class="mono" aria-label="Default value" bind:value={p.defaultValue} placeholder={p.type === 'strings' ? 'default: a, b' : p.type === 'json' ? 'default: JSON' : 'default'} />
            {/if}
            {#if p.type === 'enum'}<input type="text" class="mono" aria-label="Enum values" bind:value={p.values} placeholder="values: a, b, c" />{/if}
            <input type="text" class="desc" aria-label="Parameter description" bind:value={p.description} placeholder="Description" />
            <RowTools index={i} count={a.params.length} label="the parameter" onmove={(delta) => moveItem(a.params, i, delta)} onremove={() => a.params.splice(i, 1)} />
          </div>
        {:else}
          <p class="empty">No parameters.</p>
        {/each}
        <div class="row">
            <button type="button" class="small" onclick={() => a.params.push(emptyParam())}>+ Parameter</button>
            {#if missingParams.length}
              <button type="button" class="small" onclick={addMissingParams}>Add the connector's parameters ({missingParams.join(', ')})</button>
            {/if}
        </div>
      </section>

      <section class="card">
        <h3>Code</h3>
        <p class="hint">
          {#if a.language === 'go'}
            Declare <code>func Run(ctx *dsl.{usage?.goCtx}) error</code>; methods are PascalCase (<code>ctx.Tool()</code>).
          {:else}
            The code is the body of a function of <code>ctx</code>: it returns the result of the tool call.
          {/if}
          It runs in the hub with a 30 s limit and at most 32 connector calls, and sees the connector only through <code>ctx.call</code>.
        </p>
        {#if usage}
          <details class="ctxdoc">
            <summary>ctx API ({usage.functions.length} functions)</summary>
            <ul class="plain-list">
              {#each usage.functions as f (f.name)}
                <li><code>ctx.{a.language === 'go' ? f.name.charAt(0).toUpperCase() + f.name.slice(1) : f.name}({f.args.join(', ')})</code>{f.returns ? ` → ${f.returns}` : ''} <span class="hint">{f.doc}</span></li>
              {/each}
            </ul>
          </details>
        {/if}
        <CodeEditor bind:value={a.code} language={a.language === 'go' ? 'go' : 'javascript'} dsl="adapter" label="Adapter code" minHeight="18rem" maxHeight="40rem" />
      </section>
    </fieldset>
  {/if}
</div>

<style>
  .binding {
    display: grid;
    grid-template-columns: 1fr auto 1fr;
    gap: 0.8rem;
    align-items: start;
  }
  @media (max-width: 900px) {
    .binding {
      grid-template-columns: 1fr;
    }
    .arrow {
      display: none;
    }
  }
  .arrow {
    padding-top: 1.9rem;
    color: var(--muted);
    font-size: 1.2rem;
  }
  .links {
    display: flex;
    gap: 0.8rem;
    margin: -0.2rem 0 0.4rem;
  }
  .mark {
    display: inline-block;
    width: 1.1em;
    color: var(--muted);
  }
  .mark.ok {
    color: var(--ok, currentColor);
  }
  .prow {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
    margin-bottom: 0.3rem;
  }
  .prow input[type='text']:first-child {
    width: 10rem;
  }
  .prow select {
    width: auto;
  }
  .prow .desc {
    flex: 1;
    min-width: 10rem;
  }
  .check {
    display: inline-flex;
    gap: 0.3rem;
    align-items: center;
    font-weight: 400;
  }
  .ctxdoc {
    margin-bottom: 0.4rem;
    font-size: 0.88rem;
  }
  .plain-list {
    list-style: none;
    margin: 0.2rem 0;
    padding: 0;
    display: grid;
    gap: 0.15rem;
  }
</style>
