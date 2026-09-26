<script lang="ts">
  // Binding tab: an organisation binds an MCP to a connector (through an adapter), with the
  // connector configuration and the references of its secrets.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { mcp, errorMessage, type Binding, type Struct } from '../../api';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { openTab, closeTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';

  let { tab }: { tab: Tab } = $props();

  const org = $derived(tab.params.org ?? '');
  const isNew = $derived(!tab.params.mcp);
  let mcpName = $state('');
  let connector = $state('');
  let config = $state('');
  let secrets = $state<{ name: string; ref: string }[]>([]);
  let error = $state('');
  let saving = $state(false);
  let loadedKey = '';

  $effect(() => {
    const key = `${tab.params.org}/${tab.params.mcp}`;
    if (loadedKey === key) return;
    const b = tools.bindings.find((x) => x.orgId === tab.params.org && x.mcp === tab.params.mcp);
    if (!b && tab.params.mcp) return;
    loadedKey = key;
    mcpName = b?.mcp ?? '';
    connector = b?.connector ?? '';
    config = b?.config ? JSON.stringify(b.config, null, 2) : '';
    secrets = Object.entries(b?.secrets ?? {}).map(([name, ref]) => ({ name, ref }));
  });

  const adapters = $derived(tools.adapters.filter((a) => a.mcp === mcpName));
  const info = $derived(tools.connectors.find((c) => c.info?.id === connector)?.info);

  async function save() {
    error = '';
    let cfg: Struct | undefined;
    if (config.trim()) {
      try {
        cfg = JSON.parse(config) as Struct;
      } catch (e) {
        error = `Invalid configuration JSON (${e instanceof Error ? e.message : e})`;
        return;
      }
    }
    const out: Binding = { orgId: org, mcp: mcpName, connector, config: cfg, secrets: {} };
    for (const s of secrets) if (s.name.trim()) out.secrets![s.name.trim()] = s.ref.trim();
    saving = true;
    try {
      await mcp.bindMcp(out);
      await refreshTools();
      notify(`${mcpName} bound for ${org}`, 'ok');
      if (isNew) {
        closeTab(tab.id, { force: true });
        openTab({ kind: 'binding', params: { org, mcp: mcpName } }, { pin: true });
      }
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }

  async function unbind() {
    if (!confirm(`Unbind ${mcpName} from ${org}? Actions using it become unavailable.`)) return;
    try {
      await mcp.unbindMcp(org, mcpName);
      await refreshTools();
      closeTab(tab.id, { force: true });
    } catch (e) {
      error = errorMessage(e);
    }
  }

  provideActions(
    () => tab.id,
    () => [
      { id: 'save', label: 'Save', icon: 'save', primary: true, disabled: saving || !mcpName || !connector, run: save },
      ...(isNew ? [] : [{ id: 'unbind', label: 'Unbind', icon: 'trash' as const, danger: true, run: unbind }]),
    ],
  );
</script>

<div class="editor-page">
  <header class="head"><Icon name="key" size={18} /><h2>{org}: {mcpName || 'new binding'}</h2></header>
  {#if error}<div class="alert">{error}</div>{/if}
  <section class="card">
    <div class="field">
      <label for="b-mcp">MCP</label>
      <select id="b-mcp" bind:value={mcpName} disabled={!isNew} onchange={() => (connector = '')}>
        <option value="">—</option>
        {#each tools.mcps as m (m.name)}<option value={m.name}>{m.name}</option>{/each}
      </select>
    </div>
    <div class="field">
      <label for="b-conn">Connector (through its adapter)</label>
      <select id="b-conn" bind:value={connector}>
        <option value="">—</option>
        {#each adapters as a (a.connector)}<option value={a.connector}>{a.connector}</option>{/each}
      </select>
      {#if mcpName && !adapters.length}<span class="hint">No adapter implements {mcpName} yet: create one under the MCP first.</span>{/if}
    </div>
  </section>
  <section class="card">
    <div class="field">
      <label for="b-cfg">Connector configuration (JSON)</label>
      <textarea id="b-cfg" class="mono" rows="5" bind:value={config} placeholder={'{"root": "/data"}'}></textarea>
      {#if info?.configSchema}<pre class="mono schema">{JSON.stringify(info.configSchema, null, 2)}</pre>{/if}
    </div>
  </section>
  <section class="card">
    <h3>Secrets</h3>
    <p class="hint">Name → reference, resolved by the hub when a tool is called: <code>&lt;vault path&gt;#&lt;field&gt;</code> or <code>env:&lt;VAR&gt;</code>.{info?.secretNames?.length ? ` The connector declares: ${info.secretNames.join(', ')}.` : ''}</p>
    {#each secrets as s, i (i)}
      <div class="srow">
        <input class="mono" type="text" aria-label="Secret name" placeholder="api_key" bind:value={s.name} />
        <input class="mono grow" type="text" aria-label="Secret reference" placeholder="goap/acme#api_key" bind:value={s.ref} />
        <button type="button" class="ghost small" aria-label="Remove secret" onclick={() => secrets.splice(i, 1)}><Icon name="trash" size={14} /></button>
      </div>
    {/each}
    <button type="button" class="small" onclick={() => secrets.push({ name: '', ref: '' })}>Add a secret</button>
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
  .srow {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 0.4rem;
  }
  .schema {
    font-size: 0.8rem;
    color: var(--muted);
    overflow: auto;
  }
</style>
