<script lang="ts">
  // "Tools" explorer: connectors (services that register themselves), generic MCPs with their
  // adapters, and organisations with the MCPs they bind.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { tools, refreshTools } from '../../stores/tools.svelte';
  import { openTab, tabsState } from '../../shell/tabs.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import { iam, errorMessage } from '../../api';

  let adding = $state(false);
  let orgId = $state('');
  let orgName = $state('');
  let saving = $state(false);
  let addError = $state('');

  $effect(() => {
    if (!tools.loaded) void refreshTools();
  });

  const adaptersOf = (m: string) => tools.adapters.filter((a) => a.mcp === m);
  const bindingsOf = (o: string) => tools.bindings.filter((b) => b.orgId === o);

  async function createOrg(e: SubmitEvent) {
    e.preventDefault();
    saving = true;
    addError = '';
    try {
      const r = await iam.createOrganization(orgId.trim(), orgName.trim());
      notify(`Organisation ${r.organization?.id} created`, 'ok');
      adding = false;
      orgId = orgName = '';
      await refreshTools();
    } catch (err) {
      addError = errorMessage(err);
    } finally {
      saving = false;
    }
  }
</script>

<div class="explorer">
  <div class="tools">
    <span class="grow small muted">Connectors, MCPs, organisations</span>
    <button type="button" class="ghost small" title="New MCP" aria-label="New MCP" onclick={() => openTab({ kind: 'mcp', params: { name: '' } }, { pin: true })}
      ><Icon name="plus" size={14} /></button
    >
    <button type="button" class="ghost small" title="New organisation" aria-label="New organisation" onclick={() => (adding = !adding)}
      ><Icon name="user" size={14} /></button
    >
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={tools.loading} onclick={() => refreshTools()}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if adding}
    <form class="add" onsubmit={createOrg}>
      <label for="org-id">Organisation id (slug)</label>
      <input id="org-id" class="mono" type="text" bind:value={orgId} placeholder="acme" data-no-pin />
      <label for="org-name">Name</label>
      <input id="org-name" type="text" bind:value={orgName} placeholder="Acme Corp" data-no-pin />
      {#if addError}<div class="alert small">{addError}</div>{/if}
      <div class="row">
        <button type="submit" class="small primary" disabled={saving || !orgName.trim()}>Create</button>
        <button type="button" class="small" onclick={() => (adding = false)}>Cancel</button>
      </div>
    </form>
  {/if}
  {#if tools.error}<div class="alert small">{tools.error}</div>{/if}

  <div role="tree" aria-label="Tools">
    <TreeRow icon="radio" label="Connectors" detail={String(tools.connectors.length)} expanded={isOpen('t:conn', true)} ontoggle={() => toggle('t:conn', true)} />
    {#if isOpen('t:conn', true)}
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
      {/each}
      {#if tools.loaded && !tools.connectors.length}
        <p class="empty pad">No connector registered. A connector service registers itself with the hub.</p>
      {/if}
    {/if}

    <TreeRow icon="code" label="MCPs" detail={String(tools.mcps.length)} expanded={isOpen('t:mcp', true)} ontoggle={() => toggle('t:mcp', true)} />
    {#if isOpen('t:mcp', true)}
      {#each tools.mcps as m (m.name)}
        {@const k = `t:m:${m.name}`}
        <TreeRow
          depth={1}
          icon="book"
          label={m.name ?? '?'}
          detail={`${m.tools?.length ?? 0} tools`}
          expanded={isOpen(k, true)}
          ontoggle={() => toggle(k, true)}
          title={m.description}
          active={tabsState.active === `mcp:${m.name}`}
          onselect={() => openTab({ kind: 'mcp', params: { name: m.name ?? '' } })}
          onopen={() => openTab({ kind: 'mcp', params: { name: m.name ?? '' } }, { pin: true })}
        />
        {#if isOpen(k, true)}
          {#each adaptersOf(m.name ?? '') as a (a.connector)}
            <TreeRow
              depth={2}
              icon="branch"
              label={`via ${a.connector}`}
              detail={`${a.tools?.length ?? 0} mapped`}
              active={tabsState.active === `adapter:${a.mcp}/${a.connector}`}
              onselect={() => openTab({ kind: 'adapter', params: { mcp: a.mcp ?? '', connector: a.connector ?? '' } })}
              onopen={() => openTab({ kind: 'adapter', params: { mcp: a.mcp ?? '', connector: a.connector ?? '' } }, { pin: true })}
            />
          {/each}
          <TreeRow
            depth={2}
            icon="plus"
            label="New adapter…"
            muted
            onselect={() => openTab({ kind: 'adapter', params: { mcp: m.name ?? '', connector: '' } }, { pin: true })}
          />
        {/if}
      {/each}
    {/if}

    <TreeRow icon="user" label="Organisations" detail={String(tools.orgs.length)} expanded={isOpen('t:org', true)} ontoggle={() => toggle('t:org', true)} />
    {#if isOpen('t:org', true)}
      {#each tools.orgs as o (o.id)}
        {@const k = `t:o:${o.id}`}
        <TreeRow depth={1} icon="folder" label={o.id ?? '?'} detail={o.name ?? ''} expanded={isOpen(k, true)} ontoggle={() => toggle(k, true)} />
        {#if isOpen(k, true)}
          {#each bindingsOf(o.id ?? '') as b (b.mcp)}
            <TreeRow
              depth={2}
              icon="key"
              label={b.mcp ?? '?'}
              detail={`→ ${b.connector}`}
              active={tabsState.active === `binding:${b.orgId}/${b.mcp}`}
              onselect={() => openTab({ kind: 'binding', params: { org: b.orgId ?? '', mcp: b.mcp ?? '' } })}
              onopen={() => openTab({ kind: 'binding', params: { org: b.orgId ?? '', mcp: b.mcp ?? '' } }, { pin: true })}
            />
          {/each}
          <TreeRow
            depth={2}
            icon="plus"
            label="Bind an MCP…"
            muted
            onselect={() => openTab({ kind: 'binding', params: { org: o.id ?? '', mcp: '' } }, { pin: true })}
          />
        {/if}
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
    position: sticky;
    top: 0;
    background: var(--chrome);
    z-index: 1;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .add {
    padding: 0.2rem 0.6rem 0.6rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
