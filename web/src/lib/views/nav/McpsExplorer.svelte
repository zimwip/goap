<script lang="ts">
  // MCPs: the generic usage of a tool by an LLM (nodes of the platform namespace). An MCP knows
  // no connector; an organisational unit implements it with an adapter.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { openTab, tabsState } from '../../shell/tabs.svelte';

  $effect(() => {
    if (!tools.loaded) void refreshTools();
  });
</script>

<div class="explorer">
  <div class="tools">
    <button type="button" class="small" onclick={() => openTab({ kind: 'mcp', params: { name: '' } }, { pin: true })}><Icon name="plus" size={13} /> New MCP</button>
    <span class="grow"></span>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={tools.loading} onclick={() => refreshTools()}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if tools.error}<div class="alert small">{tools.error}</div>{/if}
  <div role="tree" aria-label="MCPs">
    {#each tools.mcps as m (m.name)}
      <TreeRow
        icon="book"
        label={m.name ?? '?'}
        detail={`${m.tools?.length ?? 0} tools`}
        title={m.description}
        active={tabsState.active === `mcp:${m.name}`}
        onselect={() => openTab({ kind: 'mcp', params: { name: m.name ?? '' } })}
        onopen={() => openTab({ kind: 'mcp', params: { name: m.name ?? '' } }, { pin: true })}
      />
    {/each}
    {#if tools.loaded && !tools.mcps.length}
      <p class="empty pad">No MCP. Create one, then attach it to an organisation.</p>
    {/if}
  </div>
</div>

<style>
  .tools {
    display: flex;
    gap: 2px;
    align-items: center;
    padding: 0 0.5rem 0.4rem;
  }
  .tools button {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
