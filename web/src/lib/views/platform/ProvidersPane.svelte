<script lang="ts">
  // Providers: connect (kind preset, protocol, API key), test by fetching the models, add them to the catalog.
  import { models, errorMessage, type CatalogModel, type DiscoveredModel, type LlmProvider, type ProviderKind } from '../../api';
  import { SvelteSet } from 'svelte/reactivity';

  let {
    providers,
    kinds,
    protocols,
    catalog,
    onchange,
    openCatalog,
  }: {
    providers: LlmProvider[];
    kinds: ProviderKind[];
    protocols: { id: string; label?: string }[];
    catalog: CatalogModel[];
    onchange: () => Promise<void> | void;
    openCatalog: () => void;
  } = $props();

  interface Form {
    isNew: boolean;
    name: string;
    kind: string;
    protocol: string;
    baseUrl: string;
    enabled: boolean;
    apiKey: string;
    clearKey: boolean;
    hasKey: boolean;
    keyHint: string;
  }

  let form = $state<Form | undefined>();
  let busy = $state('');
  let error = $state('');
  let notice = $state('');
  let found = $state<DiscoveredModel[] | undefined>();
  const picked = new SvelteSet<string>();
  let filter = $state('');

  const kindOf = (id: string) => kinds.find((k) => k.id === id);
  const modelCount = (p: string) => catalog.filter((m) => m.provider === p).length;

  function reset() {
    found = undefined;
    picked.clear();
    filter = '';
    error = '';
    notice = '';
  }

  function add(kind: ProviderKind) {
    reset();
    let name = kind.id;
    for (let i = 2; providers.some((p) => p.name === name); i++) name = `${kind.id}-${i}`;
    form = {
      isNew: true,
      name,
      kind: kind.id,
      protocol: kind.protocol ?? 'openai',
      baseUrl: kind.defaultBaseUrl ?? '',
      enabled: true,
      apiKey: '',
      clearKey: false,
      hasKey: false,
      keyHint: '',
    };
  }

  function edit(p: LlmProvider) {
    reset();
    form = {
      isNew: false,
      name: p.name,
      kind: p.kind,
      protocol: p.protocol,
      baseUrl: p.baseUrl ?? '',
      enabled: p.enabled ?? true,
      apiKey: '',
      clearKey: false,
      hasKey: !!p.hasKey,
      keyHint: p.keyHint ?? '',
    };
  }

  function close() {
    form = undefined;
    reset();
  }

  const keyRequired = $derived(form ? !!kindOf(form.kind)?.keyRequired : false);
  const canFetch = $derived(!!form && !!form.name && (!!form.apiKey || (form.hasKey && !form.clearKey) || !keyRequired));
  const valid = $derived(!!form && /^[a-z0-9][a-z0-9_-]{0,39}$/.test(form.name) && !!form.protocol);

  function toProvider(f: Form): LlmProvider {
    return { name: f.name, kind: f.kind, protocol: f.protocol, baseUrl: f.baseUrl, enabled: f.enabled };
  }

  async function saveProvider(): Promise<boolean> {
    if (!form) return false;
    const f = form;
    try {
      const saved = (await models.saveProvider(toProvider(f), f.apiKey, f.clearKey)).provider;
      // the key is kept server-side: the form now refers to the stored one
      f.apiKey = '';
      f.clearKey = false;
      f.isNew = false;
      f.hasKey = !!saved?.hasKey;
      f.keyHint = saved?.keyHint ?? '';
      await onchange();
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    }
  }

  async function save() {
    busy = 'save';
    error = '';
    notice = '';
    if (await saveProvider()) {
      notice = `Provider "${form?.name}" saved.`;
    }
    busy = '';
  }

  async function fetchModels() {
    if (!form) return;
    busy = 'fetch';
    error = '';
    notice = '';
    try {
      found = (await models.discoverModels(toProvider(form), form.apiKey)).models ?? [];
      picked.clear();
      if (!found.length) notice = 'The provider answered but lists no model.';
    } catch (e) {
      found = undefined;
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  const shown = $derived((found ?? []).filter((m) => !filter || `${m.id} ${m.displayName ?? ''}`.toLowerCase().includes(filter.toLowerCase())));

  function toggle(id: string) {
    if (picked.has(id)) picked.delete(id);
    else picked.add(id);
  }

  function selectAllShown() {
    for (const m of shown) if (!m.registered) picked.add(m.id);
  }

  async function addToCatalog() {
    if (!form || !picked.size) return;
    busy = 'add';
    error = '';
    try {
      if (!(await saveProvider())) return;
      const name = form.name;
      const byId = new Map((found ?? []).map((m) => [m.id, m]));
      for (const id of picked) {
        await models.saveModel({ provider: name, model: id, displayName: byId.get(id)?.displayName || id, enabled: true, quotaTokens: 0, quotaPeriod: 'month', roles: [] });
      }
      const n = picked.size;
      found = (found ?? []).map((m) => (picked.has(m.id) ? { ...m, registered: true } : m));
      picked.clear();
      notice = `${n} model${n > 1 ? 's' : ''} added to the catalog. Set their quota and access level in step 2.`;
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  async function toggleEnabled(p: LlmProvider) {
    error = '';
    try {
      await models.saveProvider({ ...p, enabled: !p.enabled });
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  async function remove(p: LlmProvider) {
    const n = modelCount(p.name);
    if (!confirm(`Delete the provider "${p.name}"${n ? ` and its ${n} model${n > 1 ? 's' : ''} in the catalog` : ''}? Aliases pointing to it are removed too.`)) return;
    error = '';
    try {
      await models.deleteProvider(p.name);
      if (form?.name === p.name) close();
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function onKind(id: string) {
    if (!form) return;
    const k = kindOf(id);
    form.kind = id;
    if (k) {
      form.protocol = k.protocol ?? form.protocol;
      form.baseUrl = k.defaultBaseUrl ?? '';
    }
  }
</script>

{#if error}<div class="alert">{error}</div>{/if}
{#if notice}<div class="ok-note" role="status">{notice}</div>{/if}

<section class="card">
  <div class="row head">
    <h3 class="grow">Configured providers</h3>
  </div>
  {#if providers.length === 0}
    <p class="empty">No provider yet. Pick one below to get started.</p>
  {:else}
    <div class="scroll">
      <table>
        <thead>
          <tr><th>Provider</th><th>Protocol</th><th>Endpoint</th><th>API key</th><th>Models</th><th>Status</th><th></th></tr>
        </thead>
        <tbody>
          {#each providers as p (p.name)}
            <tr class:sel={form?.name === p.name}>
              <td><strong>{p.name}</strong> <span class="hint">{kindOf(p.kind)?.label ?? p.kind}</span></td>
              <td><code>{p.protocol}</code></td>
              <td class="url"><code>{p.baseUrl || 'default'}</code></td>
              <td>{#if p.hasKey}<code>{p.keyHint}</code>{:else}<span class="hint">none</span>{/if}</td>
              <td>
                <button type="button" class="link" onclick={openCatalog}>{modelCount(p.name)}</button>
              </td>
              <td>
                {#if !p.enabled}<span class="st off">disabled</span>
                {:else if p.active}<span class="st on">active</span>
                {:else}<span class="st bad" title="Enabled but not loaded: check the API key and the endpoint">not loaded</span>{/if}
              </td>
              <td class="actions">
                <button type="button" class="small" onclick={() => edit(p)}>Edit</button>
                <button type="button" class="small" onclick={() => toggleEnabled(p)}>{p.enabled ? 'Disable' : 'Enable'}</button>
                <button type="button" class="small danger" onclick={() => remove(p)}>Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

{#if !form}
  <section class="card">
    <h3>Add a provider</h3>
    <div class="kinds">
      {#each kinds as k (k.id)}
        <button type="button" class="kind" onclick={() => add(k)}>
          <strong>{k.label ?? k.id}</strong>
          <span class="hint">{k.description}</span>
          <code>{k.protocol}</code>
        </button>
      {/each}
    </div>
  </section>
{:else}
  <form
    class="card"
    onsubmit={(e) => {
      e.preventDefault();
      void save();
    }}
  >
    <h3>{form.isNew ? 'New provider' : `Edit ${form.name}`} <span class="hint">{kindOf(form.kind)?.label ?? form.kind}</span></h3>
    <div class="grid">
      <div class="field">
        <label for="pv-name">Name</label>
        <input id="pv-name" type="text" class="mono" bind:value={form.name} disabled={!form.isNew} spellcheck="false" />
        <span class="hint">Used in model names: <code>{form.name || 'name'}/model</code></span>
      </div>
      <div class="field">
        <label for="pv-kind">Preset</label>
        <select id="pv-kind" value={form.kind} onchange={(e) => onKind(e.currentTarget.value)}>
          {#each kinds as k (k.id)}<option value={k.id}>{k.label ?? k.id}</option>{/each}
        </select>
      </div>
      <div class="field">
        <label for="pv-proto">Protocol</label>
        <select id="pv-proto" bind:value={form.protocol}>
          {#each protocols as p (p.id)}<option value={p.id}>{p.label ?? p.id}</option>{/each}
        </select>
      </div>
      <div class="field wide">
        <label for="pv-url">Base URL</label>
        <input id="pv-url" type="url" class="mono" bind:value={form.baseUrl} placeholder="provider default" spellcheck="false" />
      </div>
      <div class="field wide">
        <label for="pv-key">API key {#if !keyRequired}<span class="hint">(optional)</span>{/if}</label>
        <input
          id="pv-key"
          type="password"
          autocomplete="off"
          class="mono"
          bind:value={form.apiKey}
          placeholder={form.hasKey && !form.clearKey ? `stored ${form.keyHint} — leave empty to keep it` : 'paste the key'}
        />
        {#if form.hasKey}
          <label class="inline"><input type="checkbox" bind:checked={form.clearKey} /> Remove the stored key</label>
        {/if}
        <span class="hint">Stored encrypted, never shown again.</span>
      </div>
      <label class="inline"><input type="checkbox" bind:checked={form.enabled} /> Enabled</label>
    </div>

    <div class="row">
      <button type="submit" class="primary" disabled={!valid || busy !== ''}>{busy === 'save' ? 'Saving…' : 'Save'}</button>
      <button type="button" disabled={!canFetch || !valid || busy !== ''} onclick={fetchModels}>
        {busy === 'fetch' ? 'Contacting the provider…' : 'Test & fetch models'}
      </button>
      <span class="grow"></span>
      <button type="button" class="ghost" onclick={close}>Close</button>
    </div>

    {#if found}
      <div class="found">
        <div class="row">
          <strong class="grow">{found.length} model{found.length === 1 ? '' : 's'} offered by the provider</strong>
          <input type="search" placeholder="Filter…" bind:value={filter} aria-label="Filter models" />
          <button type="button" class="small" onclick={selectAllShown}>Select shown</button>
          <button type="button" class="primary" disabled={!picked.size || busy !== ''} onclick={addToCatalog}>
            {busy === 'add' ? 'Adding…' : `Add ${picked.size || ''} to the catalog`}
          </button>
        </div>
        <ul class="models">
          {#each shown as m (m.id)}
            <li>
              <label>
                <input type="checkbox" checked={m.registered || picked.has(m.id)} disabled={m.registered} onchange={() => toggle(m.id)} />
                <code>{m.id}</code>
                {#if m.displayName && m.displayName !== m.id}<span class="hint">{m.displayName}</span>{/if}
                {#if m.registered}<span class="st on">in catalog</span>{/if}
              </label>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </form>
{/if}

<style>
  .scroll {
    overflow-x: auto;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin-top: 0.6rem;
  }
  .row.head {
    margin-top: 0;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  .url code {
    word-break: break-all;
  }
  tr.sel {
    background: var(--hover);
  }
  .kinds {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 0.6rem;
  }
  .kind {
    display: grid;
    gap: 0.25rem;
    text-align: left;
    align-content: start;
    padding: 0.6rem 0.75rem;
    font-weight: 400;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
    gap: 0 1rem;
  }
  .field.wide {
    grid-column: 1 / -1;
  }
  .inline {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-weight: 400;
  }
  .st {
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.8rem;
    border: 1px solid var(--border);
  }
  .st.on {
    color: var(--ok);
    border-color: var(--ok);
  }
  .st.bad {
    color: var(--danger);
    border-color: var(--danger);
  }
  .st.off {
    color: var(--muted);
  }
  .link {
    border: none;
    background: none;
    color: var(--accent);
    padding: 0;
    min-height: 0;
    text-decoration: underline;
  }
  .found {
    margin-top: 1rem;
    border-top: 1px solid var(--border);
    padding-top: 0.5rem;
  }
  .models {
    list-style: none;
    margin: 0.5rem 0 0;
    padding: 0;
    max-height: 22rem;
    overflow: auto;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 0.1rem 0.8rem;
  }
  .models label {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-weight: 400;
    padding: 0.15rem 0;
  }
  .ok-note {
    border: 1px solid var(--ok);
    border-radius: 6px;
    padding: 0.4rem 0.7rem;
    margin-bottom: 0.7rem;
    color: var(--ok);
  }
</style>
