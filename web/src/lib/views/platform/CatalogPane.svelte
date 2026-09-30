<script lang="ts">
  // Catalog: which models the platform exposes, their global token quota and who may use them, and the aliases.
  // Edits are staged (a personal change of the platform namespace) and saved from the dialog's Save bar; an alias
  // that a methodology references and nobody configured is proposed by an open change and reviewed here.
  import { errorMessage, formatInt, type CatalogModel, type LlmProvider, type ModelAlias } from '../../api';
  import {
    saveModel,
    deleteModel,
    saveAlias as saveAliasNode,
    deleteAlias,
    acceptAliasProposal,
    declineAliasProposal,
    type AliasProposal,
    type Unsaved,
  } from '../../llmEdit';
  import { confirmDialog } from '../../shell/confirmState.svelte';

  let {
    providers,
    catalog,
    aliases,
    proposals = [],
    onchange,
    openProviders,
  }: {
    providers: LlmProvider[];
    catalog: Unsaved<CatalogModel>[];
    aliases: Unsaved<ModelAlias>[];
    proposals?: AliasProposal[];
    onchange: () => Promise<void> | void;
    openProviders: () => void;
  } = $props();

  // Roles a model can be restricted to. Administrators always have access.
  const ROLES = ['contributor', 'methodologist', 'approver', 'release_manager'];
  const PERIODS = [
    ['day', 'per day'],
    ['month', 'per month'],
    ['total', 'in total'],
  ];

  const key = (m: { provider: string; model: string }) => `${m.provider}/${m.model}`;
  const num = (v: string | number | undefined) => Number(v ?? 0) || 0;

  interface Row {
    m: CatalogModel;
    quota: number;
    period: string;
    roles: string[];
    enabled: boolean;
  }

  // Local edits, keyed by model; a row is dirty when it differs from the stored entry.
  let edits = $state<Record<string, Row>>({});
  let error = $state('');
  let busy = $state('');
  let filter = $state('');

  const rows = $derived(
    catalog
      .filter((m) => !filter || key(m).toLowerCase().includes(filter.toLowerCase()) || (m.displayName ?? '').toLowerCase().includes(filter.toLowerCase()))
      .map((m) => edits[key(m)] ?? fresh(m)),
  );

  function fresh(m: CatalogModel): Row {
    return { m, quota: num(m.quotaTokens), period: m.quotaPeriod || 'month', roles: [...(m.roles ?? [])], enabled: !!m.enabled };
  }

  function row(m: CatalogModel): Row {
    const k = key(m);
    if (!edits[k]) edits[k] = fresh(m);
    return edits[k];
  }

  function dirty(r: Row): boolean {
    const m = r.m;
    return (
      r.quota !== num(m.quotaTokens) ||
      r.period !== (m.quotaPeriod || 'month') ||
      r.enabled !== !!m.enabled ||
      r.roles.slice().sort().join() !== (m.roles ?? []).slice().sort().join()
    );
  }

  function toggleRole(r: Row, role: string) {
    const x = row(r.m);
    x.roles = x.roles.includes(role) ? x.roles.filter((v) => v !== role) : [...x.roles, role];
    void save(x);
  }

  async function save(r: Row) {
    busy = key(r.m);
    error = '';
    try {
      await saveModel({ ...r.m, enabled: r.enabled, quotaTokens: r.quota, quotaPeriod: r.period, roles: r.roles });
      delete edits[key(r.m)];
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  async function remove(m: CatalogModel) {
    if (!(await confirmDialog({ message: `Remove ${key(m)} from the catalog? Aliases pointing to it are removed too.`, danger: true }))) return;
    error = '';
    try {
      await deleteModel(m.provider, m.model);
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function level(r: Row): string {
    return r.roles.length ? r.roles.join(', ') : 'everyone';
  }

  function pct(r: Row): number {
    return r.quota > 0 ? Math.min(100, Math.round((num(r.m.usedTokens) / r.quota) * 100)) : 0;
  }

  // --- aliases ---------------------------------------------------------------------

  let aliasName = $state('');
  let aliasTarget = $state('');

  async function saveAlias(alias: string, target: string) {
    const [provider, ...rest] = target.split('/');
    error = '';
    try {
      await saveAliasNode({ alias, provider, model: rest.join('/') });
      aliasName = '';
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  // proposals: the target the reviewer picks (defaults to the proposed one)
  let proposalTarget = $state<Record<string, string>>({});

  async function accept(p: AliasProposal) {
    busy = p.impactId;
    error = '';
    try {
      await acceptAliasProposal(p, proposalTarget[p.impactId] || p.target);
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  async function decline(p: AliasProposal) {
    busy = p.impactId;
    error = '';
    try {
      await declineAliasProposal(p);
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  async function removeAlias(alias: string) {
    error = '';
    try {
      await deleteAlias(alias);
      await onchange();
    } catch (e) {
      error = errorMessage(e);
    }
  }
</script>

{#if error}<div class="alert">{error}</div>{/if}

<section class="card">
  <div class="row head">
    <h3 class="grow">Model catalog</h3>
    {#if catalog.length > 6}<input type="search" placeholder="Filter…" bind:value={filter} aria-label="Filter models" />{/if}
  </div>
  <p class="hint">
    Only models listed here can be called. The quota is a global budget shared by every user (input + output tokens).
    Access level: leave every role unchecked to allow any signed-in user; administrators always have access.
  </p>
  {#if catalog.length === 0}
    <p class="empty">
      The catalog is empty. <button type="button" class="link" onclick={openProviders}>Add a provider and fetch its models</button>.
    </p>
  {:else}
    <div class="scroll">
      <table>
        <thead>
          <tr><th>Model</th><th>Enabled</th><th>Global quota (tokens)</th><th>Used</th><th>Who may use it</th><th></th></tr>
        </thead>
        <tbody>
          {#each rows as r (key(r.m))}
            <tr class:dirty={dirty(r) || (r.m as Unsaved<CatalogModel>).pending} class:off={!r.enabled}>
              <td>
                <code>{key(r.m)}</code>
                {#if r.m.displayName && r.m.displayName !== r.m.model}<div class="hint">{r.m.displayName}</div>{/if}
              </td>
              <td>
                <input type="checkbox" checked={r.enabled} onchange={(e) => { row(r.m).enabled = e.currentTarget.checked; void save(row(r.m)); }} aria-label={`Enable ${key(r.m)}`} />
              </td>
              <td class="quota">
                <div class="quota-controls">
                  <input
                    type="number"
                    min="0"
                    step="1000"
                    value={r.quota}
                    placeholder="0"
                    aria-label={`Quota of ${key(r.m)}`}
                    onchange={(e) => { row(r.m).quota = Math.max(0, Math.floor(Number(e.currentTarget.value) || 0)); void save(row(r.m)); }}
                  />
                  <select value={r.period} aria-label="Quota period" onchange={(e) => { row(r.m).period = e.currentTarget.value; void save(row(r.m)); }}>
                    {#each PERIODS as [v, l] (v)}<option value={v}>{l}</option>{/each}
                  </select>
                </div>
                {#if r.quota === 0}<span class="hint">unlimited</span>{/if}
              </td>
              <td class="used">
                {#if r.quota > 0}
                  <div class="bar" class:full={pct(r) >= 100} title={`${formatInt(num(r.m.usedTokens))} of ${formatInt(r.quota)}`}>
                    <span style={`width:${pct(r)}%`}></span>
                  </div>
                  <span class="hint">{formatInt(num(r.m.usedTokens))} ({pct(r)}%)</span>
                {:else}
                  <span class="hint">{formatInt(num(r.m.usedTokens))}</span>
                {/if}
              </td>
              <td>
                <details>
                  <summary>{level(r)}</summary>
                  <div class="roles">
                    {#each ROLES as role (role)}
                      <label><input type="checkbox" checked={r.roles.includes(role)} onchange={() => toggleRole(r, role)} /> {role}</label>
                    {/each}
                  </div>
                </details>
              </td>
              <td class="actions">
                {#if (r.m as Unsaved<CatalogModel>).pending}<span class="pending" title="Not saved yet">unsaved</span>{/if}
                <button type="button" class="small danger" onclick={() => remove(r.m)}>Remove</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<section class="card">
  <h3>Aliases</h3>
  <p class="hint">
    Callers ask for an alias (<code>default</code>, <code>fast</code>…) instead of a precise model, so a model can be swapped here without touching methodologies.
  </p>
  {#if proposals.length}
    <ul class="aliases proposals">
      {#each proposals as p (p.impactId)}
        <li>
          <code>{p.alias}</code> →
          <select bind:value={proposalTarget[p.impactId]} aria-label={`Target of ${p.alias}`}>
            <option value="" disabled>model…</option>
            {#each catalog as m (key(m))}<option value={key(m)}>{key(m)}</option>{/each}
          </select>
          <button type="button" class="small primary" disabled={!(proposalTarget[p.impactId] ?? p.target) || busy !== ''} onclick={() => accept(p)}>Accept</button>
          <button type="button" class="small danger" disabled={busy !== ''} onclick={() => decline(p)}>Decline</button>
          <span class="hint">proposed{p.by ? ` by ${p.by}` : ''}{p.reason ? `: ${p.reason}` : ''}</span>
        </li>
      {/each}
    </ul>
  {/if}
  {#if aliases.length}
    <ul class="aliases">
      {#each aliases as a (a.alias)}
        <li>
          <code>{a.alias}</code>{#if a.pending} <span class="pending" title="Not saved yet">unsaved</span>{/if} →
          <select value={`${a.provider}/${a.model}`} aria-label={`Target of ${a.alias}`} onchange={(e) => saveAlias(a.alias, e.currentTarget.value)}>
            {#each catalog as m (key(m))}<option value={key(m)}>{key(m)}</option>{/each}
          </select>
          <button type="button" class="small danger" onclick={() => removeAlias(a.alias)}>Remove</button>
        </li>
      {/each}
    </ul>
  {/if}
  {#if catalog.length}
    <form
      class="row"
      onsubmit={(e) => {
        e.preventDefault();
        if (aliasName && aliasTarget) void saveAlias(aliasName, aliasTarget);
      }}
    >
      <input type="text" class="mono" placeholder="alias (e.g. default)" bind:value={aliasName} aria-label="New alias" spellcheck="false" />
      <select bind:value={aliasTarget} aria-label="Alias target">
        <option value="" disabled>model…</option>
        {#each catalog as m (key(m))}<option value={key(m)}>{key(m)}</option>{/each}
      </select>
      <button type="submit" disabled={!aliasName || !aliasTarget}>Add / update alias</button>
    </form>
  {/if}
</section>

{#if providers.length === 0}
  <p class="hint">No provider configured: <button type="button" class="link" onclick={openProviders}>connect one first</button>.</p>
{/if}

<style>
  .pending {
    display: inline-block;
    margin-right: 0.4rem;
    padding: 0 0.4rem;
    border: 1px solid var(--warn);
    border-radius: 999px;
    color: var(--warn);
    font-size: 0.75rem;
  }
  .proposals {
    border-left: 3px solid var(--warn);
    padding-left: 0.6rem;
    margin-bottom: 0.6rem;
  }
  .scroll {
    overflow-x: auto;
  }
  table {
    min-width: 52rem;
  }
  th,
  td {
    padding: 0.5rem 0.6rem;
    vertical-align: top;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .row.head {
    margin-bottom: 0.3rem;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  tr.dirty {
    background: var(--hover);
  }
  tr.off code {
    opacity: 0.55;
  }
  .quota-controls {
    display: flex;
    gap: 0.35rem;
    flex-wrap: wrap;
  }
  .quota input[type='number'] {
    width: 7rem;
  }
  .used {
    min-width: 8rem;
  }
  .bar {
    height: 6px;
    border-radius: 3px;
    background: var(--surface-2);
    overflow: hidden;
    margin-bottom: 0.15rem;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .bar.full span {
    background: var(--danger);
  }
  details summary {
    cursor: pointer;
  }
  .roles {
    display: grid;
    gap: 0.3rem;
    padding: 0.4rem 0;
  }
  .roles label {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    font-weight: 400;
  }
  .aliases {
    list-style: none;
    padding: 0;
    margin: 0 0 0.6rem;
    display: grid;
    gap: 0.35rem;
  }
  .aliases li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .link {
    border: none;
    background: none;
    color: var(--accent);
    padding: 0;
    min-height: 0;
    text-decoration: underline;
  }
</style>
