<script lang="ts">
  // History of a node: every version with its state, the properties that
  // changed, the change that produced it (ADR 0014).
  import { graph, errorMessage, formatDate, shortId, type GraphNode } from '../api';
  import { openTab } from '../shell/tabs.svelte';
  import { changes, refreshChanges } from '../stores/catalog.svelte';

  let { id, reloadKey = 0 }: { id: string; /** bump to reload */ reloadKey?: number } = $props();

  let versions = $state<GraphNode[]>([]);
  let loading = $state(true);
  let error = $state('');
  let open = $state<number[]>([]);

  async function load(signal?: AbortSignal) {
    loading = true;
    error = '';
    try {
      versions = ((await graph.listNodeVersions(id, signal)).versions ?? []).slice().sort((a, b) => (a.version ?? 0) - (b.version ?? 0));
      if (!changes.loaded) void refreshChanges();
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  $effect(() => {
    void reloadKey;
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  });

  const latest = $derived(versions.at(-1));
  const changeTitle = (cid: string | undefined) => (cid ? (changes.items.find((c) => c.id === cid)?.title ?? `change ${shortId(cid)}`) : 'import (no change)');

  interface Entry {
    v: GraphNode;
    /** state before this version ('' : none) */
    from: string;
    moved: boolean;
    /** properties added / changed / removed compared to the version it descends from */
    diff: { key: string; before: string; after: string }[];
  }

  const text = (x: unknown) => (x === undefined || x === null ? '' : typeof x === 'string' ? x : JSON.stringify(x));

  const entries = $derived.by<Entry[]>(() => {
    const byVersion = new Map(versions.map((v) => [v.version ?? 0, v]));
    return versions.map((v, i) => {
      const parent = byVersion.get(v.parents?.[0] ?? -1) ?? versions[i - 1];
      const before = (parent?.props ?? {}) as Record<string, unknown>;
      const after = (v.props ?? {}) as Record<string, unknown>;
      const diff = [...new Set([...Object.keys(before), ...Object.keys(after)])]
        .filter((k) => JSON.stringify(before[k]) !== JSON.stringify(after[k]))
        .sort()
        .map((k) => ({ key: k, before: text(before[k]), after: text(after[k]) }));
      const from = parent?.state ?? '';
      return { v, from, moved: !!parent && (v.state ?? '') !== from, diff: parent ? diff : [] };
    });
  });

  /** consecutive distinct states, oldest first */
  const path = $derived(
    versions.map((v) => v.state ?? '').filter((s, i, all) => s && s !== all[i - 1]),
  );

  const shown = $derived([...entries].reverse());

  function toggle(v: number) {
    open = open.includes(v) ? open.filter((x) => x !== v) : [...open, v];
  }
</script>

<div class="history">
  {#if error}<div class="alert">{error}</div>{/if}
  {#if loading && !versions.length}
    <p class="empty">Loading…</p>
  {:else if versions.length}
    <p class="hint">{versions.length} version{versions.length > 1 ? 's' : ''}{latest?.state ? ` · now ${latest.state}` : ''}{latest?.deleted ? ' · deleted' : ''}.</p>
    {#if path.length > 1}
      <ol class="path" aria-label="States over time">
        {#each path as s, i (i)}<li><span class="state">{s}</span></li>{/each}
      </ol>
    {/if}
    <section class="card">
      <table>
        <thead><tr><th>Version</th><th>State</th><th>How</th><th>Change</th><th>Date</th><th>Properties</th></tr></thead>
        <tbody>
          {#each shown as e (e.v.version)}
            <tr class:deleted={e.v.deleted}>
              <td><strong>v{e.v.version}</strong>{#if e.v.branch && e.v.branch !== 'main'} <span class="hint">{e.v.branch}</span>{/if}</td>
              <td>
                {#if e.v.state}
                  {#if e.moved}<span class="from">{e.from || 'none'} →</span>{/if}
                  <span class="state" class:moved={e.moved}>{e.v.state}</span>
                {:else}
                  <span class="hint">—</span>
                {/if}
              </td>
              <td>{e.v.deleted ? 'deleted' : (e.v.reason ?? '')}{#if (e.v.parents?.length ?? 0) > 1} <span class="hint">of v{e.v.parents?.join(' + v')}</span>{/if}</td>
              <td>
                {#if e.v.changeId}
                  <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: e.v.changeId ?? '' } }, { pin: true })}>{changeTitle(e.v.changeId)}</button>
                {:else}
                  <span class="hint">{changeTitle(undefined)}</span>
                {/if}
              </td>
              <td>{formatDate(e.v.createdAt)}</td>
              <td>
                <button type="button" class="link" aria-expanded={open.includes(e.v.version ?? 0)} onclick={() => toggle(e.v.version ?? 0)}>
                  {e.diff.length ? `${e.diff.length} changed` : e.v.version === versions[0]?.version ? 'initial' : e.moved ? 'state only' : 'none'}
                </button>
              </td>
            </tr>
            {#if open.includes(e.v.version ?? 0)}
              <tr class="detail">
                <td colspan="6">
                  {#if e.diff.length}
                    <table class="diff">
                      <tbody>
                        {#each e.diff as d (d.key)}
                          <tr><th>{d.key}</th><td class="before">{d.before}</td><td class="arrow">→</td><td class="after">{d.after}</td></tr>
                        {/each}
                      </tbody>
                    </table>
                  {/if}
                  <details>
                    <summary>All properties of v{e.v.version}</summary>
                    <pre>{JSON.stringify(e.v.props ?? {}, null, 2)}</pre>
                  </details>
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </section>
  {:else}
    <p class="empty">No version found.</p>
  {/if}
</div>

<style>
  .path {
    list-style: none;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.2rem;
    padding: 0;
    margin: 0.3rem 0 0.8rem;
  }
  .path li + li::before {
    content: '→';
    color: var(--muted);
    margin-right: 0.2rem;
  }
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.85rem;
    font-family: var(--mono);
  }
  .state.moved {
    border-color: var(--accent);
    color: var(--accent);
  }
  .from {
    color: var(--muted);
    font-size: 0.85rem;
    margin-right: 0.2rem;
  }
  tr.deleted td {
    opacity: 0.7;
  }
  .detail td {
    background: var(--surface-2);
  }
  .diff {
    width: auto;
  }
  .diff th {
    text-align: left;
    font-family: var(--mono);
    padding-right: 0.8rem;
  }
  .diff .before {
    color: var(--danger);
    text-decoration: line-through;
  }
  .diff .after {
    color: var(--ok);
  }
  .diff .arrow {
    color: var(--muted);
  }
  pre {
    margin: 0.4rem 0 0;
  }
</style>
