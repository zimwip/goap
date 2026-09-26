<script lang="ts">
  // Connector tab (read-only): what a registered connector announced.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { formatDate } from '../../api';
  import { provideActions } from '../../shell/workbench.svelte';

  let { tab }: { tab: Tab } = $props();

  const c = $derived(tools.connectors.find((x) => x.info?.id === tab.params.id));
  const info = $derived(c?.info);

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: tools.loading, run: () => refreshTools() }],
  );
</script>

<div class="editor-page">
  {#if info}
    <header class="head">
      <Icon name="zap" size={18} />
      <h2>{info.id} <span class="muted">v{info.version}</span></h2>
      <StatusBadge status={c?.live ? 'completed' : 'failed'} />
      <span class="muted">{c?.live ? 'live' : 'registration expired'}</span>
    </header>
    <section class="card">
      <dl class="kv">
        {#if info.description}<dt>Description</dt><dd>{info.description}</dd>{/if}
        <dt>Endpoint</dt><dd><code>{c?.endpoint}</code></dd>
        <dt>Last seen</dt><dd>{formatDate(c?.lastSeen)}</dd>
        {#if info.secretNames?.length}<dt>Secrets</dt><dd>{info.secretNames.join(', ')}</dd>{/if}
      </dl>
      <p class="hint">A connector is a separate service: it registers itself with the hub and renews its registration as a heartbeat.</p>
    </section>
    {#if info.configSchema}
      <section class="card">
        <h3>Configuration (per organisation)</h3>
        <pre class="mono">{JSON.stringify(info.configSchema, null, 2)}</pre>
      </section>
    {/if}
    <section class="card">
      <h3>Operations</h3>
      {#each info.operations ?? [] as op (op.name)}
        <div class="op">
          <code>{op.name}</code>
          <span class="muted">{op.description}</span>
          {#if op.inputSchema}<pre class="mono">{JSON.stringify(op.inputSchema, null, 2)}</pre>{/if}
        </div>
      {/each}
    </section>
  {:else}
    <p class="empty">Connector {tab.params.id} is not registered.</p>
  {/if}
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
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.25rem 1rem;
  }
  .kv dt {
    color: var(--muted);
  }
  .kv dd {
    margin: 0;
  }
  .op {
    margin-bottom: 0.7rem;
  }
  pre {
    margin: 0.3rem 0 0;
    overflow: auto;
    font-size: 0.82rem;
  }
</style>
