<script lang="ts">
  // Platform-wide algorithm registry (ADR 0041): algorithms centralized from the domains that declared them, on
  // publish, grouped by type then algorithm then the domain instances that reference it — the same organisation as
  // the per-domain Algorithms explorer (AlgorithmExplorer.svelte), minus the editing (this is read-only: a domain
  // still declares its algorithms directly, only the registry centralizes them).
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { toggle, isOpen } from '../nav/expanded.svelte';
  import { registry, errorMessage, type PlatformAlgorithm } from '../../api';
  import { ALGORITHM_USAGES } from '../../dsl';

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

  const q = $derived(filter.trim().toLowerCase());
  const match = (s: string | undefined) => !q || (s ?? '').toLowerCase().includes(q);
  const matches = (p: PlatformAlgorithm) =>
    match(p.algorithm?.name) || match(p.sourceDomain) || (p.instances ?? []).some((u) => match(u.domain) || match(u.instance));

  const sorted = $derived([...algorithms].sort((a, b) => (a.algorithm?.name ?? '').localeCompare(b.algorithm?.name ?? '')));
  const usages = $derived(ALGORITHM_USAGES.filter((u) => u.usage !== 'adapter'));

  function forType(type: string) {
    return sorted.filter((p) => p.algorithm?.type === type && matches(p));
  }

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

  const valuesTitle = (v: Record<string, unknown> | undefined) => (v && Object.keys(v).length ? JSON.stringify(v) : '');
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
  <section class="card tree-card">
    <div class="row head">
      <h3 class="grow">Algorithms</h3>
      {#if algorithms.length > 6}<input type="search" placeholder="Filter…" bind:value={filter} aria-label="Filter algorithms" />{/if}
    </div>
    {#if loading && !algorithms.length}
      <p class="empty">Loading…</p>
    {:else if algorithms.length === 0}
      <p class="empty">No algorithm has been centralized yet.</p>
    {:else}
      <div role="tree" aria-label="Platform algorithms">
        {#each usages as u (u.usage)}
          {@const gk = `platformalg:${u.usage}`}
          {@const algs = forType(u.usage)}
          <TreeRow icon="code" label={u.title} detail={String(algs.length)} title={u.description} expanded={isOpen(gk, true)} ontoggle={() => toggle(gk, true)} />
          {#if isOpen(gk, true)}
            {#each algs as p (p.algorithm?.name)}
              {@const name = p.algorithm?.name ?? ''}
              {@const ak = `platformalg:${u.usage}:${name}`}
              {@const insts = p.instances ?? []}
              <TreeRow
                depth={1}
                icon="zap"
                label={`platform@${name}`}
                detail={`${p.algorithm?.language ?? ''} · ${p.sourceDomain}@${p.sourceVersion}`}
                title={p.algorithm?.description || name}
                expanded={insts.length ? isOpen(ak, false) : undefined}
                ontoggle={() => toggle(ak, false)}
                badge={insts.length || undefined}
              >
                {#snippet actions()}
                  <button type="button" title="Copy the platform@ reference" aria-label={`Copy platform@${name}`} onclick={(e) => { e.stopPropagation(); copyRef(name); }}>
                    <Icon name={copied === name ? 'check' : 'copy'} size={12} />
                  </button>
                {/snippet}
              </TreeRow>
              {#if insts.length && isOpen(ak, false)}
                {#each insts as x, k (x.domain + '/' + x.version + '/' + x.instance + k)}
                  <TreeRow depth={2} icon="tag" label={x.instance || '(unnamed)'} detail={`${x.domain}@${x.version}`} title={valuesTitle(x.values) || undefined} />
                {/each}
              {/if}
            {:else}
              <p class="empty pad3">No {u.title.toLowerCase()} algorithms.</p>
            {/each}
          {/if}
        {/each}
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
  .tree-card {
    padding-bottom: 0.4rem;
  }
  .pad3 {
    padding: 0.1rem 0 0.1rem 46px;
    margin: 0;
    font-size: 0.9em;
  }
</style>
