<script lang="ts">
  // MCP tab: a generic MCP definition (name, description, tools with their JSON schemas).
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { mcp, errorMessage, type Mcp, type Struct } from '../../api';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { openTab, closeTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';

  let { tab }: { tab: Tab } = $props();

  interface ToolRow {
    name: string;
    description: string;
    schema: string;
  }

  const isNew = $derived(!tab.params.name);
  let name = $state('');
  let description = $state('');
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
    rows = (m?.tools ?? []).map((t) => ({
      name: t.name ?? '',
      description: t.description ?? '',
      schema: t.inputSchema ? JSON.stringify(t.inputSchema, null, 2) : '',
    }));
  });

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
      out.tools!.push({ name: r.name.trim(), description: r.description.trim(), inputSchema: schema });
    }
    saving = true;
    try {
      await mcp.saveMcp(out);
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
    if (!confirm(`Delete the MCP ${name}?`)) return;
    try {
      await mcp.deleteMcp(name);
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
  </section>
  <section class="card">
    <h3>Tools</h3>
    <p class="hint">Generic signatures. A connector implements them through an adapter, and an organisation binds the MCP to a connector.</p>
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
        <button type="button" class="ghost small" aria-label="Remove tool" onclick={() => rows.splice(i, 1)}><Icon name="trash" size={14} /></button>
      </div>
      <div class="field">
        <label for="t-{i}-s">Input schema (JSON)</label>
        <textarea id="t-{i}-s" class="mono" rows="4" bind:value={r.schema}></textarea>
      </div>
    {/each}
    <button type="button" class="small" onclick={() => rows.push({ name: '', description: '', schema: '' })}>Add a tool</button>
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
</style>
