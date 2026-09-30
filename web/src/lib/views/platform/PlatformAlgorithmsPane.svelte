<script lang="ts">
  // Platform-wide algorithm registry (ADR 0041): algorithms centralized from the domains that declared them, on
  // publish. Read-only here (a domain still declares its algorithms directly; this only lists what got centralized)
  // — no staged edits, so no save bar involvement.
  import Icon from '../../shell/Icon.svelte';
  import { registry, errorMessage, type PlatformAlgorithm } from '../../api';

  let algorithms = $state<PlatformAlgorithm[]>([]);
  let loading = $state(true);
  let error = $state('');
  let filter = $state('');
  let copied = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      algorithms = (await registry.listPlatformAlgorithms()).algorithms ?? [];
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void load();
  });

  const rows = $derived(
    algorithms
      .filter((p) => {
        const name = p.algorithm?.name ?? '';
        return !filter || name.toLowerCase().includes(filter.toLowerCase()) || (p.sourceDomain ?? '').toLowerCase().includes(filter.toLowerCase());
      })
      .sort((a, b) => (a.algorithm?.name ?? '').localeCompare(b.algorithm?.name ?? '')),
  );

  async function copyRef(name: string) {
    const ref = `platform@${name}`;
    try {
      await navigator.clipboard.writeText(ref);
      copied = name;
      setTimeout(() => { if (copied === name) copied = ''; }, 1200);
    } catch {
      // clipboard access denied: nothing to fall back to
    }
  }
</script>

<div class="pane">
  <div class="head">
    <p class="hint grow">
      An algorithm a domain declares is centralized here on publish, so another domain can reference it as
      <code>platform@&lt;name&gt;</code> in an algorithm instance instead of redeclaring it.
    </p>
    <button type="button" class="small refresh" disabled={loading} onclick={load}><Icon name="refresh" size={13} />Refresh</button>
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  <section class="card">
    <div class="row head">
      <h3 class="grow">Algorithms</h3>
      {#if algorithms.length > 6}<input type="search" placeholder="Filter…" bind:value={filter} aria-label="Filter algorithms" />{/if}
    </div>
    {#if loading && !algorithms.length}
      <p class="empty">Loading…</p>
    {:else if rows.length === 0}
      <p class="empty">{algorithms.length === 0 ? 'No algorithm has been centralized yet.' : 'No match.'}</p>
    {:else}
      <div class="scroll">
        <table>
          <thead>
            <tr><th>Reference</th><th>Type</th><th>Language</th><th>Declared by</th></tr>
          </thead>
          <tbody>
            {#each rows as p (p.algorithm?.name)}
              <tr>
                <td>
                  <button type="button" class="ref" title="Copy the platform@ reference" onclick={() => copyRef(p.algorithm?.name ?? '')}>
                    <code>platform@{p.algorithm?.name}</code>
                    <Icon name="copy" size={12} />
                  </button>
                  {#if copied === p.algorithm?.name}<span class="hint copied">copied</span>{/if}
                  {#if p.algorithm?.description}<div class="hint">{p.algorithm.description}</div>{/if}
                </td>
                <td><code>{p.algorithm?.type}</code></td>
                <td><code>{p.algorithm?.language}</code></td>
                <td>{p.sourceDomain}@{p.sourceVersion}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
</div>

<style>
  .pane {
    display: grid;
    gap: 0.8rem;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .row.head {
    margin-bottom: 0.3rem;
  }
  .grow {
    flex: 1;
  }
  .refresh {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    white-space: nowrap;
  }
  .scroll {
    overflow-x: auto;
  }
  th,
  td {
    padding: 0.5rem 0.6rem;
    vertical-align: top;
  }
  .ref {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    border: none;
    background: none;
    padding: 0;
    min-height: 0;
    color: inherit;
    font: inherit;
  }
  .ref:hover {
    color: var(--accent);
  }
  .copied {
    margin-left: 0.3rem;
    color: var(--accent);
  }
</style>
