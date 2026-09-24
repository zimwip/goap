<script lang="ts">
  // Explorer of shared domains: domain → version → node types / link types.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { domains, refreshDomains, groupedDomains, getDomainDraft, peekDomainDraft, domainKey } from '../../stores/domains.svelte';
  import { openTab, tabsState, tabId } from '../../shell/tabs.svelte';
  import { select } from '../../shell/workbench.svelte';
  import { formatDate, type DomainSummary } from '../../api';
  import { domainSpec, openDomain, revealDomainPath } from '../editors/domainTabs';
  import { emptyNodeType, emptyLinkType } from '../../methodologyForm';
  import { openContextMenu } from '../../shell/contextMenuState.svelte';

  let filter = $state('');

  $effect(() => {
    if (!domains.loaded) void refreshDomains();
  });

  const groups = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    const all = groupedDomains();
    if (!q) return all;
    return all.filter((g) => `${g.name} ${g.description}`.toLowerCase().includes(q));
  });

  const vkey = (v: DomainSummary) => domainKey(v.name ?? '', v.version ?? '');

  function toggleVersion(v: DomainSummary) {
    const k = `dv:${vkey(v)}`;
    toggle(k);
    if (isOpen(k)) void getDomainDraft(v.name ?? '', v.version ?? '');
  }

  // Expanded versions (restored state) load their draft.
  $effect(() => {
    for (const v of domains.items) if (isOpen(`dv:${vkey(v)}`)) getDomainDraft(v.name ?? '', v.version ?? '');
  });

  function selectVersion(v: DomainSummary, pin = false) {
    openDomain(v.name ?? '', v.version ?? '', pin);
    select({
      title: `${v.name} v${v.version}`,
      subtitle: 'Domain',
      rows: [
        ['Status', v.status ?? ''],
        ['Description', v.description ?? ''],
        ['Node types', String(v.nodeTypeCount ?? 0)],
        ['Link types', String(v.linkTypeCount ?? 0)],
        ['Modified', formatDate(v.updatedAt)],
        ['Published', formatDate(v.publishedAt)],
      ],
    });
  }

  function addNodeType(v: DomainSummary) {
    const d = getDomainDraft(v.name ?? '', v.version ?? '');
    d.form.nodeTypes.push(emptyNodeType());
    revealDomainPath(d.name, d.version, `nodeTypes[${d.form.nodeTypes.length - 1}]`);
  }

  function addLinkType(v: DomainSummary) {
    const d = getDomainDraft(v.name ?? '', v.version ?? '');
    d.form.linkTypes.push(emptyLinkType());
    revealDomainPath(d.name, d.version, `linkTypes[${d.form.linkTypes.length - 1}]`);
  }
</script>

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filter…" aria-label="Filter domains" bind:value={filter} data-no-pin />
    <button type="button" class="ghost small" title="New domain" aria-label="New domain" onclick={() => openTab(domainSpec('', ''), { pin: true })}
      ><Icon name="plus" size={14} /></button
    >
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={domains.loading} onclick={() => refreshDomains()}
      ><Icon name="refresh" size={14} /></button
    >
  </div>

  {#if domains.error}<div class="alert small">{domains.error}</div>{/if}
  {#if domains.loaded && !domains.items.length && !domains.error}
    <p class="empty pad">No domains. Create one: methodologies reference it for their node and link types.</p>
  {/if}

  <div role="tree" aria-label="Domains">
    {#each groups as g (g.name)}
      {@const gk = `dm:${g.name}`}
      <TreeRow icon="graph" label={g.name} expanded={isOpen(gk, true)} title={g.description || g.name} ontoggle={() => toggle(gk, true)} />
      {#if isOpen(gk, true)}
        {#each g.versions as v (v.version)}
          {@const k = vkey(v)}
          {@const d = peekDomainDraft(k)}
          {@const vOpen = isOpen(`dv:${k}`)}
          {@const vid = tabId(domainSpec(v.name ?? '', v.version ?? ''))}
          <TreeRow
            depth={1}
            icon="tag"
            label={`v${v.version}`}
            detail={`${v.nodeTypeCount ?? 0} types · ${v.linkTypeCount ?? 0} links`}
            expanded={vOpen}
            active={tabsState.active === vid}
            badge={d?.dirty ? '●' : d && d.allIssues.length ? d.allIssues.length : undefined}
            badgeTone={d?.dirty ? 'accent' : 'danger'}
            onselect={() => selectVersion(v)}
            onopen={() => selectVersion(v, true)}
            ontoggle={() => toggleVersion(v)}
          >
            {#snippet trail()}
              <StatusBadge status={v.status} />
            {/snippet}
          </TreeRow>
          {#if vOpen}
            {#if !d || d.loading}
              <p class="empty pad2">Loading…</p>
            {:else if d.loadError}
              <p class="alert small">{d.loadError}</p>
            {:else}
              {@const ntK = `dn:${k}`}
              <TreeRow
                depth={2}
                icon="node"
                label="Node types"
                detail={String(d.form.nodeTypes.length)}
                expanded={isOpen(ntK, true)}
                badge={d.count('nodeTypes') || undefined}
                badgeTone="danger"
                ontoggle={() => toggle(ntK, true)}
                oncontextmenu={(e) => openContextMenu(e, [{ label: 'New node type', icon: 'plus', disabled: d.readonly, run: () => addNodeType(v) }])}
              >
                {#snippet actions()}
                  {#if !d.readonly}
                    <button type="button" title="Add" aria-label="Add: Node type" onclick={(e) => { e.stopPropagation(); addNodeType(v); }}
                      ><Icon name="plus" size={13} /></button
                    >
                  {/if}
                {/snippet}
              </TreeRow>
              {#if isOpen(ntK, true)}
                {#each d.form.nodeTypes as n, i (i)}
                  <TreeRow
                    depth={3}
                    label={n.name || '(unnamed)'}
                    italic={!n.name}
                    detail={n.extends ? `extends ${n.extends}` : ''}
                    badge={d.count(`nodeTypes[${i}]`) || undefined}
                    badgeTone="danger"
                    onselect={() => revealDomainPath(d.name, d.version, `nodeTypes[${i}]`)}
                    onopen={() => revealDomainPath(d.name, d.version, `nodeTypes[${i}]`)}
                    oncontextmenu={(e) =>
                      openContextMenu(e, [
                        {
                          label: 'Remove node type',
                          icon: 'trash',
                          disabled: d.readonly,
                          run: () => {
                            if (confirm(`Remove the node type "${n.name || 'unnamed'}" from the draft?`)) d.form.nodeTypes.splice(i, 1);
                          },
                        },
                      ])}
                  />
                {:else}
                  <p class="empty pad3">No node types.</p>
                {/each}
              {/if}
              {@const ltK = `dl:${k}`}
              <TreeRow
                depth={2}
                icon="trace"
                label="Link types"
                detail={String(d.form.linkTypes.length)}
                expanded={isOpen(ltK, true)}
                badge={d.count('linkTypes') || undefined}
                badgeTone="danger"
                ontoggle={() => toggle(ltK, true)}
                oncontextmenu={(e) => openContextMenu(e, [{ label: 'New link type', icon: 'plus', disabled: d.readonly, run: () => addLinkType(v) }])}
              >
                {#snippet actions()}
                  {#if !d.readonly}
                    <button type="button" title="Add" aria-label="Add: Link type" onclick={(e) => { e.stopPropagation(); addLinkType(v); }}
                      ><Icon name="plus" size={13} /></button
                    >
                  {/if}
                {/snippet}
              </TreeRow>
              {#if isOpen(ltK, true)}
                {#each d.form.linkTypes as l, i (i)}
                  <TreeRow
                    depth={3}
                    label={l.name || '(unnamed)'}
                    italic={!l.name}
                    detail={l.from && l.to ? `${l.from} → ${l.to}` : ''}
                    badge={d.count(`linkTypes[${i}]`) || undefined}
                    badgeTone="danger"
                    onselect={() => revealDomainPath(d.name, d.version, `linkTypes[${i}]`)}
                    onopen={() => revealDomainPath(d.name, d.version, `linkTypes[${i}]`)}
                    oncontextmenu={(e) =>
                      openContextMenu(e, [
                        {
                          label: 'Remove link type',
                          icon: 'trash',
                          disabled: d.readonly,
                          run: () => {
                            if (confirm(`Remove the link type "${l.name || 'unnamed'}" from the draft?`)) d.form.linkTypes.splice(i, 1);
                          },
                        },
                      ])}
                  />
                {:else}
                  <p class="empty pad3">No link types.</p>
                {/each}
              {/if}
            {/if}
          {/if}
        {/each}
      {/if}
    {/each}
  </div>
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
    position: sticky;
    top: 0;
    background: var(--chrome);
    z-index: 1;
  }
  .tools input {
    min-height: 24px;
    height: 24px;
    margin-right: 0.2rem;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .pad2 {
    padding: 0.1rem 0 0.1rem 46px;
    margin: 0;
  }
  .pad3 {
    padding: 0.1rem 0 0.1rem 58px;
    margin: 0;
    font-size: 0.9em;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
