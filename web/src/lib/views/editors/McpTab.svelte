<script lang="ts">
  // MCP tab: a generic MCP definition (name, description, scope, tools with their JSON schemas), stored as a
  // node `MCP:<name>` of the platform namespace and edited through a change applied on main.
  import { types as nodeTypes, ns } from '../../stores/session.svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { errorMessage, type Mcp, type McpScope, type Struct } from '../../api';
  import { headGraph, findNode, applyOnMain, createNodeItem, updateNodeItem, deleteNodeItem } from '../../graphEdit';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { openTab, closeTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';

  let { tab }: { tab: Tab } = $props();

  interface ToolRow {
    name: string;
    description: string;
    schema: string;
    readOnly: boolean;
  }

  $effect(() => {
    if (!tools.loaded) void refreshTools();
  });

  const isNew = $derived(!tab.params.name);
  let name = $state('');
  let description = $state('');
  let scope = $state<McpScope>('both');
  let rows = $state<ToolRow[]>([]);
  let error = $state('');
  let saving = $state(false);
  let loadedKey = '';

  $effect(() => {
    const key = tab.params.name ?? '';
    if (loadedKey === key && (isNew || rows.length || name)) return;
    const m = tools.mcps.find((x) => x.name === key);
    if (!m && key) return;
    loadedKey = key;
    name = m?.name ?? '';
    description = m?.description ?? '';
    scope = m?.scope || 'both';
    rows = (m?.tools ?? []).map((t) => ({
      name: t.name ?? '',
      description: t.description ?? '',
      schema: t.inputSchema ? JSON.stringify(t.inputSchema, null, 2) : '',
      readOnly: !!t.readOnly,
    }));
  });

  const keyOf = (n: string) => `MCP:${n}`;

  async function save() {
    error = '';
    const out: Mcp = { name: name.trim(), description: description.trim(), tools: [] };
    for (const r of rows) {
      let schema: Struct | undefined;
      if (r.schema.trim()) {
        try {
          schema = JSON.parse(r.schema) as Struct;
        } catch (e) {
          error = `Tool ${r.name}: invalid JSON schema (${e instanceof Error ? e.message : e})`;
          return;
        }
      }
      out.tools!.push({ name: r.name.trim(), description: r.description.trim(), ...(schema ? { inputSchema: schema } : {}), ...(r.readOnly ? { readOnly: true } : {}) });
    }
    saving = true;
    try {
      const h = await headGraph(ns.platform);
      const existing = findNode(h, ns.platform, nodeTypes.mcp, keyOf(out.name!));
      // a null value clears the property: both is the default scope
      const props: Struct = { name: out.name ?? '', description: out.description ?? '', scope: scope === 'both' ? null : scope, tools: (out.tools ?? []) as unknown as Struct[] };
      const item = existing ? updateNodeItem(existing, props) : createNodeItem(keyOf(out.name!), nodeTypes.mcp, props);
      await applyOnMain(ns.platform, `MCP ${out.name}`, `${existing ? 'Update' : 'Create'} MCP ${out.name}`, h.baselineId, [item]);
      await refreshTools();
      notify(`MCP ${out.name} saved`, 'ok');
      if (isNew) {
        closeTab(tab.id, { force: true });
        openTab({ kind: 'mcp', params: { name: out.name ?? '' } }, { pin: true });
      }
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!(await confirmDialog({ message: `Delete the MCP ${name}? Adapters that implement it stop working.`, danger: true }))) return;
    try {
      const h = await headGraph(ns.platform);
      const existing = findNode(h, ns.platform, nodeTypes.mcp, keyOf(name));
      if (!existing) throw new Error(`MCP ${name} is not on the graph`);
      await applyOnMain(ns.platform, `Delete MCP ${name}`, `Delete MCP ${name}`, h.baselineId, [deleteNodeItem(existing)]);
      await refreshTools();
      closeTab(tab.id, { force: true });
    } catch (e) {
      error = errorMessage(e);
    }
  }

  provideActions(
    () => tab.id,
    () => [
      { id: 'save', label: 'Save', icon: 'save', primary: true, disabled: saving || !name.trim(), run: save },
      ...(isNew ? [] : [{ id: 'delete', label: 'Delete', icon: 'trash' as const, danger: true, run: remove }]),
    ],
  );
</script>

<div class="editor-page">
  <header class="head"><Icon name="book" size={18} /><h2>{isNew ? 'New MCP' : name}</h2></header>
  {#if error}<div class="alert">{error}</div>{/if}
  <section class="card">
    <div class="field">
      <label for="mcp-name">Name</label>
      <input id="mcp-name" class="mono" type="text" bind:value={name} disabled={!isNew} placeholder="document-repository" />
    </div>
    <div class="field">
      <label for="mcp-desc">Description</label>
      <input id="mcp-desc" type="text" bind:value={description} />
    </div>
    <div class="field">
      <label for="mcp-scope">Scope</label>
      <select id="mcp-scope" bind:value={scope}>
        <option value="both">action and agent</option>
        <option value="action">action only</option>
        <option value="agent">agent only</option>
      </select>
      <span class="hint">Where a methodology may declare the MCP: on its actions (<code>actions[].mcps</code>, tool actions), on its agents (<code>agents[].mcps</code>, reached by the agent's llm actions), or both. Orchestration tools, such as starting other agents, belong to the agent level.</span>
    </div>
  </section>
  <section class="card">
    <h3>Tools</h3>
    <p class="hint">Generic signatures, as an LLM uses them. An MCP knows no connector: an organisational unit implements it with an adapter (Organisation, MCP pane).</p>
    {#each rows as r, i (i)}
      <div class="trow">
        <div class="field">
          <label for="t-{i}-n">Tool name</label>
          <input id="t-{i}-n" class="mono" type="text" bind:value={r.name} />
        </div>
        <div class="field grow">
          <label for="t-{i}-d">Description</label>
          <input id="t-{i}-d" type="text" bind:value={r.description} />
        </div>
        <label class="ro"><input type="checkbox" bind:checked={r.readOnly} /> read-only</label>
        <button type="button" class="ghost small" aria-label="Remove tool" onclick={() => rows.splice(i, 1)}><Icon name="trash" size={14} /></button>
      </div>
      <div class="field">
        <label for="t-{i}-s">Input schema (JSON)</label>
        <textarea id="t-{i}-s" class="mono" rows="4" bind:value={r.schema}></textarea>
      </div>
    {/each}
    <button type="button" class="small" onclick={() => rows.push({ name: '', description: '', schema: '', readOnly: false })}>Add a tool</button>
  </section>
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
  .trow {
    display: flex;
    gap: 0.6rem;
    align-items: flex-end;
  }
  .ro {
    white-space: nowrap;
    padding-bottom: 0.4rem;
  }
</style>
