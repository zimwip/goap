<script lang="ts">
  // Adapters: MCPs (what an LLM uses), connectors (the real services) and the adapters that join them, managed
  // together. MCPs and adapters are graph nodes of the platform namespace, changed through changes; a connector
  // is a runtime service that registers itself with the hub.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { tabsState, openTab } from '../../shell/tabs.svelte';

  $effect(() => {
    if (!tools.loaded) void refreshTools();
  });

  const newMcp = () => openTab({ kind: 'mcp', params: { name: '' } }, { pin: true });
  const newAdapter = () => openTab({ kind: 'adapter', params: { name: '' } }, { pin: true });
</script>

<div class="explorer">
  <div class="tools">
    <span class="grow small muted">Platform tool layer</span>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={tools.loading} onclick={() => refreshTools()}><Icon name="refresh" size={14} /></button>
  </div>
  {#if tools.error}<div class="alert small">{tools.error}</div>{/if}

  <div role="tree" aria-label="Adapters, MCPs and connectors">
    <TreeRow
      icon="code"
      label="Adapters"
      detail={String(tools.adapterDefs.length)}
      title="Implement the tools of an MCP with the operations of a connector"
      expanded={isOpen('adp:adapters', true)}
      ontoggle={() => toggle('adp:adapters', true)}
    >
      {#snippet actions()}
        <button type="button" title="New adapter" aria-label="New adapter" onclick={(e) => { e.stopPropagation(); newAdapter(); }}><Icon name="plus" size={13} /></button>
      {/snippet}
    </TreeRow>
    {#if isOpen('adp:adapters', true)}
      {#each tools.adapterDefs as d (d.name)}
        <TreeRow
          depth={1}
          icon="zap"
          label={d.name}
          detail={`${d.mcp || '?'} → ${d.connector || '?'}`}
          title={d.description || d.name}
          active={tabsState.active === `adapter:${d.name}`}
          onselect={() => openTab({ kind: 'adapter', params: { name: d.name } })}
          onopen={() => openTab({ kind: 'adapter', params: { name: d.name } }, { pin: true })}
        />
      {:else}
        {#if tools.loaded}<p class="empty pad3">No adapter. An MCP is usable by a unit only once an adapter implements it.</p>{/if}
      {/each}
    {/if}

    <TreeRow
      icon="book"
      label="MCPs"
      detail={String(tools.mcps.length)}
      title="Generic usage of a tool by an LLM"
      expanded={isOpen('adp:mcps', true)}
      ontoggle={() => toggle('adp:mcps', true)}
          >
      {#snippet actions()}
        <button type="button" title="New MCP" aria-label="New MCP" onclick={(e) => { e.stopPropagation(); newMcp(); }}><Icon name="plus" size={13} /></button>
      {/snippet}
    </TreeRow>
    {#if isOpen('adp:mcps', true)}
      {#each tools.mcps as m (m.name)}
        <TreeRow
          depth={1}
          icon="book"
          label={m.name ?? '?'}
          detail={`${m.tools?.length ?? 0} tools`}
          title={m.description}
          active={tabsState.active === `mcp:${m.name}`}
          onselect={() => openTab({ kind: 'mcp', params: { name: m.name ?? '' } })}
          onopen={() => openTab({ kind: 'mcp', params: { name: m.name ?? '' } }, { pin: true })}
        />
      {:else}
        {#if tools.loaded}<p class="empty pad3">No MCP.</p>{/if}
      {/each}
    {/if}

    <TreeRow
      icon="zap"
      label="Connectors"
      detail={String(tools.connectors.length)}
      title="Services that register themselves with the MCP hub"
      expanded={isOpen('adp:connectors', true)}
      ontoggle={() => toggle('adp:connectors', true)}
    />
    {#if isOpen('adp:connectors', true)}
      {#each tools.connectors as c (c.info?.id)}
        <TreeRow
          depth={1}
          icon="zap"
          label={c.info?.id ?? '?'}
          detail={c.info?.version ? `v${c.info.version}` : ''}
          badge={c.live ? 'live' : 'expired'}
          badgeTone={c.live ? 'ok' : 'danger'}
          title={`${c.info?.description ?? ''}\n${c.endpoint ?? ''}`}
          active={tabsState.active === `connector:${c.info?.id}`}
          onselect={() => openTab({ kind: 'connector', params: { id: c.info?.id ?? '' } })}
          onopen={() => openTab({ kind: 'connector', params: { id: c.info?.id ?? '' } }, { pin: true })}
        />
      {:else}
        {#if tools.loaded}<p class="empty pad3">No connector registered. A connector is a service that registers itself with the hub.</p>{/if}
      {/each}
    {/if}
  </div>
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    align-items: center;
    padding: 0 0.5rem 0.4rem;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .pad3 {
    padding: 0.1rem 0.8rem 0.1rem 46px;
    margin: 0;
    font-size: 0.9em;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
