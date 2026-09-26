<script lang="ts">
  // Adapter tab: how a connector implements an MCP, tool by tool (declarative mapping).
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { mcp, errorMessage, type Adapter, type Struct } from '../../api';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { openTab, closeTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';

  let { tab }: { tab: Tab } = $props();

  interface Row {
    tool: string;
    operation: string;
    args: string;
    resultPath: string;
  }

  const mcpName = $derived(tab.params.mcp ?? '');
  const isNew = $derived(!tab.params.connector);
  const def = $derived(tools.mcps.find((m) => m.name === mcpName));
  let connector = $state('');
  let rows = $state<Row[]>([]);
  let error = $state('');
  let warnings = $state<string[]>([]);
  let saving = $state(false);
  let loadedKey = '';

  $effect(() => {
    const key = `${tab.params.mcp}/${tab.params.connector}`;
    if (loadedKey === key) return;
    const a = tools.adapters.find((x) => x.mcp === tab.params.mcp && x.connector === tab.params.connector);
    if (!a && tab.params.connector) return;
    loadedKey = key;
    connector = a?.connector ?? '';
    rows = (a?.tools ?? []).map((t) => ({
      tool: t.tool ?? '',
      operation: t.operation ?? '',
      args: t.arguments ? JSON.stringify(t.arguments, null, 2) : '',
      resultPath: t.resultPath ?? '',
    }));
  });

  const registered = $derived(tools.connectors.find((c) => c.info?.id === connector));
  const operations = $derived(registered?.info?.operations?.map((o) => o.name ?? '') ?? []);

  async function save() {
    error = '';
    warnings = [];
    const out: Adapter = { mcp: mcpName, connector: connector.trim(), tools: [] };
    for (const r of rows) {
      let args: Struct | undefined;
      if (r.args.trim()) {
        try {
          args = JSON.parse(r.args) as Struct;
        } catch (e) {
          error = `Tool ${r.tool}: invalid arguments JSON (${e instanceof Error ? e.message : e})`;
          return;
        }
      }
      out.tools!.push({ tool: r.tool, operation: r.operation.trim(), arguments: args, resultPath: r.resultPath.trim() });
    }
    saving = true;
    try {
      const r = await mcp.saveAdapter(out);
      warnings = r.warnings ?? [];
      await refreshTools();
      notify(`Adapter ${mcpName}/${out.connector} saved`, 'ok');
      if (isNew) {
        closeTab(tab.id, { force: true });
        openTab({ kind: 'adapter', params: { mcp: mcpName, connector: out.connector ?? '' } }, { pin: true });
      }
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!confirm(`Delete the adapter ${mcpName}/${connector}?`)) return;
    try {
      await mcp.deleteAdapter(mcpName, connector);
      await refreshTools();
      closeTab(tab.id, { force: true });
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function addMissing() {
    for (const t of def?.tools ?? []) if (!rows.some((r) => r.tool === t.name)) rows.push({ tool: t.name ?? '', operation: '', args: '', resultPath: '' });
  }

  provideActions(
    () => tab.id,
    () => [
      { id: 'save', label: 'Save', icon: 'save', primary: true, disabled: saving || !connector.trim(), run: save },
      ...(isNew ? [] : [{ id: 'delete', label: 'Delete', icon: 'trash' as const, danger: true, run: remove }]),
    ],
  );
</script>

<div class="editor-page">
  <header class="head"><Icon name="branch" size={18} /><h2>Adapter {mcpName}{connector ? ` via ${connector}` : ''}</h2></header>
  {#if error}<div class="alert">{error}</div>{/if}
  {#each warnings as w (w)}<div class="alert warn">{w}</div>{/each}
  <section class="card">
    <div class="field">
      <label for="ad-conn">Connector</label>
      <input id="ad-conn" class="mono" type="text" list="ad-conns" bind:value={connector} disabled={!isNew} placeholder="localfs" />
      <datalist id="ad-conns">{#each tools.connectors as c (c.info?.id)}<option value={c.info?.id}></option>{/each}</datalist>
      {#if connector && !registered}<span class="hint">This connector is not registered: its operations cannot be checked.</span>{/if}
    </div>
  </section>
  <section class="card">
    <h3>Tool mappings</h3>
    <p class="hint">
      Arguments: literals, or <code>"$.name"</code> references to the arguments of the tool call. Empty: the arguments are passed as they are.
      Result path: dotted path picked in the operation result (empty: all of it). Unmapped tools are unavailable through this adapter.
    </p>
    {#each rows as r, i (i)}
      <div class="mrow">
        <div class="field">
          <label for="m-{i}-t">Tool</label>
          <select id="m-{i}-t" bind:value={r.tool}>
            {#each def?.tools ?? [] as t (t.name)}<option value={t.name}>{t.name}</option>{/each}
          </select>
        </div>
        <div class="field">
          <label for="m-{i}-o">Operation</label>
          <input id="m-{i}-o" class="mono" type="text" list="ops-{i}" bind:value={r.operation} />
          <datalist id="ops-{i}">{#each operations as o (o)}<option value={o}></option>{/each}</datalist>
        </div>
        <div class="field">
          <label for="m-{i}-r">Result path</label>
          <input id="m-{i}-r" class="mono" type="text" bind:value={r.resultPath} />
        </div>
        <button type="button" class="ghost small" aria-label="Remove mapping" onclick={() => rows.splice(i, 1)}><Icon name="trash" size={14} /></button>
      </div>
      <div class="field">
        <label for="m-{i}-a">Arguments (JSON)</label>
        <textarea id="m-{i}-a" class="mono" rows="3" bind:value={r.args} placeholder={'{"path": "$.path"}'}></textarea>
      </div>
    {/each}
    <button type="button" class="small" onclick={addMissing}>Add the unmapped tools</button>
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
  .mrow {
    display: flex;
    gap: 0.6rem;
    align-items: flex-end;
    flex-wrap: wrap;
  }
</style>
