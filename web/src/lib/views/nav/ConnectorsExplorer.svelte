<script lang="ts">
  // Connectors: separate services that register themselves with the hub (read-only).
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
    <span class="grow small muted">Registered connectors</span>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={tools.loading} onclick={() => refreshTools()}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if tools.error}<div class="alert small">{tools.error}</div>{/if}
  <div role="tree" aria-label="Connectors">
    {#each tools.connectors as c (c.info?.id)}
      <TreeRow
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
    {/each}
    {#if tools.loaded && !tools.connectors.length}
      <p class="empty pad">No connector registered. A connector is a service that registers itself with the MCP hub.</p>
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
    padding: 0.1rem 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
