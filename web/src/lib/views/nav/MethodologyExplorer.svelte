<script lang="ts">
  // Explorer: methodology → version → sections (Agents, Actions,
  // Conditions, Goals, Domain) → items.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { expanded, toggle, isOpen } from './expanded.svelte';
  import { methodologies, refreshMethodologies, groupedMethodologies } from '../../stores/catalog.svelte';
  import { getDraft, peekDraft, draftKey, type Draft } from '../../stores/drafts.svelte';
  import { openTab, tabsState, tabId } from '../../shell/tabs.svelte';
  import { select, requestReveal } from '../../shell/workbench.svelte';
  import { formatDate, type MethodologySummary } from '../../api';
  import {
    emptyAgent,
    emptyAction,
    emptyCondition,
    emptyGoal,
    emptyNodeType,
    emptyLinkType,
    type Section,
    type SectionItem,
  } from '../../methodologyForm';
  import {
    SECTION_LABEL,
    SECTION_ICON,
    itemSpec,
    methodologySpec,
    openItem,
  } from '../editors/methodologyTabs';

  let filter = $state('');

  $effect(() => {
    if (!methodologies.loaded) void refreshMethodologies();
  });

  const groups = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    const all = groupedMethodologies();
    if (!q) return all;
    return all.filter((g) => `${g.name} ${g.description}`.toLowerCase().includes(q));
  });

  const SECTIONS: Section[] = ['agents', 'actions', 'conditions', 'goals'];

  function vkey(v: MethodologySummary) {
    return draftKey(v.name ?? '', v.version ?? '');
  }

  function toggleVersion(v: MethodologySummary) {
    const k = `v:${vkey(v)}`;
    toggle(k);
    if (expanded[k]) void getDraft(v.name ?? '', v.version ?? '');
  }

  // Expanded versions (restored state) load their draft.
  $effect(() => {
    for (const m of methodologies.items) if (isOpen(`v:${vkey(m)}`)) getDraft(m.name ?? '', m.version ?? '');
  });

  function selectVersion(v: MethodologySummary, pin = false) {
    openTab(methodologySpec(v.name ?? '', v.version ?? ''), { pin });
    select({
      title: `${v.name} v${v.version}`,
      subtitle: 'Methodology',
      rows: [
        ['Status', v.status ?? ''],
        ['Description', v.description ?? ''],
        ['Agents', (v.agents ?? []).map((a) => a.name).join(', ') || '—'],
        ['Goals', (v.goals ?? []).map((g) => g.name).join(', ') || '—'],
        ['Modified', formatDate(v.updatedAt)],
        ['Published', formatDate(v.publishedAt)],
      ],
    });
  }

  function add(d: Draft, section: Section) {
    const factories = { agents: emptyAgent, actions: emptyAction, conditions: emptyCondition, goals: emptyGoal };
    const item = factories[section]();
    (d.form[section] as SectionItem[]).push(item);
    expanded[`s:${d.key}/${section}`] = true;
    openItem(d, section, d.form[section][d.form[section].length - 1], true);
  }

  function itemDetail(section: Section, it: SectionItem): string {
    if (section === 'actions' && 'kind' in it) return it.kind;
    if (section === 'agents' && 'planner' in it) return it.planner;
    return '';
  }

  function revealDomain(d: Draft, path: string) {
    const t = openTab(methodologySpec(d.name, d.version));
    requestReveal(t.id, path);
  }

  function openDomain(d: Draft) {
    revealDomain(d, 'nodeTypes');
  }

  function addNodeType(d: Draft) {
    d.form.nodeTypes.push(emptyNodeType());
    expanded[`s:${d.key}/nodeTypes`] = true;
    revealDomain(d, `nodeTypes[${d.form.nodeTypes.length - 1}]`);
  }

  function addLinkType(d: Draft) {
    d.form.linkTypes.push(emptyLinkType());
    expanded[`s:${d.key}/linkTypes`] = true;
    revealDomain(d, `linkTypes[${d.form.linkTypes.length - 1}]`);
  }
</script>

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filter…" aria-label="Filter methodologies" bind:value={filter} data-no-pin />
    <button
      type="button"
      class="ghost small"
      title="New methodology"
      aria-label="New methodology"
      onclick={() => openTab(methodologySpec('', ''), { pin: true })}><Icon name="plus" size={14} /></button
    >
    <button
      type="button"
      class="ghost small"
      title="Import YAML"
      aria-label="Import YAML"
      onclick={() => openTab({ kind: 'import', params: {} }, { pin: true })}><Icon name="upload" size={14} /></button
    >
    <button
      type="button"
      class="ghost small"
      title="Refresh"
      aria-label="Refresh"
      disabled={methodologies.loading}
      onclick={() => refreshMethodologies()}><Icon name="refresh" size={14} /></button
    >
  </div>

  {#if methodologies.error}<div class="alert small">{methodologies.error}</div>{/if}
  {#if methodologies.loaded && !methodologies.items.length && !methodologies.error}
    <p class="empty pad">No methodologies. Create one or import a YAML file.</p>
  {/if}

  <div role="tree" aria-label="Methodologies">
    {#each groups as g (g.name)}
      {@const gk = `m:${g.name}`}
      <TreeRow
        icon="book"
        label={g.name}
        expanded={isOpen(gk, true)}
        title={g.description || g.name}
        ontoggle={() => toggle(gk, true)}
      />
      {#if isOpen(gk, true)}
        {#each g.versions as v (v.version)}
          {@const k = vkey(v)}
          {@const d = peekDraft(k)}
          {@const vOpen = isOpen(`v:${k}`)}
          {@const vid = tabId(methodologySpec(v.name ?? '', v.version ?? ''))}
          <TreeRow
              depth={1}
              icon="tag"
              label={`v${v.version}`}
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
              {#each SECTIONS as s (s)}
                {@const sk = `s:${k}/${s}`}
                {@const items = d.items(s)}
                {@const n = d.count(s)}
                <TreeRow
                  depth={2}
                  icon={SECTION_ICON[s]}
                  label={SECTION_LABEL[s]}
                  detail={String(items.length)}
                  expanded={isOpen(sk)}
                  badge={n || undefined}
                  badgeTone="danger"
                  ontoggle={() => toggle(sk)}
                >
                  {#snippet actions()}
                    {#if !d.readonly}
                      <button
                        type="button"
                        title="Add"
                        aria-label={`Add: ${SECTION_LABEL[s]}`}
                        onclick={(e) => {
                          e.stopPropagation();
                          add(d, s);
                        }}><Icon name="plus" size={13} /></button
                      >
                    {/if}
                  {/snippet}
                </TreeRow>
                {#if isOpen(sk)}
                  {#each items as it, i (it.uid)}
                    {@const id = tabId(itemSpec(d, s, it))}
                    {@const issues = d.count(`${s}[${i}]`)}
                    {@const dirty = d.itemDirty(s, it.uid)}
                    <TreeRow
                      depth={3}
                      label={it.name || '(unnamed)'}
                      italic={!it.name}
                      detail={itemDetail(s, it)}
                      active={tabsState.active === id}
                      badge={issues || (dirty ? '●' : undefined)}
                      badgeTone={issues ? 'danger' : 'accent'}
                      onselect={() => openItem(d, s, it)}
                      onopen={() => openItem(d, s, it, true)}
                    />
                  {:else}
                    <p class="empty pad3">
                      {s === 'agents' ? 'No agent (default agent: all actions).' : 'No items.'}
                    </p>
                  {/each}
                {/if}
              {/each}
              {@const domK = `s:${k}/domain`}
              <TreeRow
                depth={2}
                icon="graph"
                label="Domain"
                detail={`${d.form.nodeTypes.length} types · ${d.form.linkTypes.length} links`}
                expanded={isOpen(domK)}
                badge={d.count('nodeTypes') + d.count('linkTypes') || undefined}
                badgeTone="danger"
                onselect={() => openDomain(d)}
                onopen={() => openDomain(d)}
                ontoggle={() => toggle(domK)}
              />
              {#if isOpen(domK)}
                {@const ntK = `s:${k}/nodeTypes`}
                <TreeRow
                  depth={3}
                  icon="node"
                  label="Node types"
                  detail={String(d.form.nodeTypes.length)}
                  expanded={isOpen(ntK)}
                  badge={d.count('nodeTypes') || undefined}
                  badgeTone="danger"
                  ontoggle={() => toggle(ntK)}
                >
                  {#snippet actions()}
                    {#if !d.readonly}
                      <button
                        type="button"
                        title="Add"
                        aria-label="Add: Node type"
                        onclick={(e) => {
                          e.stopPropagation();
                          addNodeType(d);
                        }}><Icon name="plus" size={13} /></button
                      >
                    {/if}
                  {/snippet}
                </TreeRow>
                {#if isOpen(ntK)}
                  {#each d.form.nodeTypes as n, i (i)}
                    <TreeRow
                      depth={4}
                      label={n.name || '(unnamed)'}
                      italic={!n.name}
                      detail={n.extends ? `extends ${n.extends}` : ''}
                      badge={d.count(`nodeTypes[${i}]`) || undefined}
                      badgeTone="danger"
                      onselect={() => revealDomain(d, `nodeTypes[${i}]`)}
                      onopen={() => revealDomain(d, `nodeTypes[${i}]`)}
                    />
                  {:else}
                    <p class="empty pad3">No node types.</p>
                  {/each}
                {/if}
                {@const ltK = `s:${k}/linkTypes`}
                <TreeRow
                  depth={3}
                  icon="trace"
                  label="Link types"
                  detail={String(d.form.linkTypes.length)}
                  expanded={isOpen(ltK)}
                  badge={d.count('linkTypes') || undefined}
                  badgeTone="danger"
                  ontoggle={() => toggle(ltK)}
                >
                  {#snippet actions()}
                    {#if !d.readonly}
                      <button
                        type="button"
                        title="Add"
                        aria-label="Add: Link type"
                        onclick={(e) => {
                          e.stopPropagation();
                          addLinkType(d);
                        }}><Icon name="plus" size={13} /></button
                      >
                    {/if}
                  {/snippet}
                </TreeRow>
                {#if isOpen(ltK)}
                  {#each d.form.linkTypes as l, i (i)}
                    <TreeRow
                      depth={4}
                      label={l.name || '(unnamed)'}
                      italic={!l.name}
                      detail={l.from && l.to ? `${l.from} → ${l.to}` : ''}
                      badge={d.count(`linkTypes[${i}]`) || undefined}
                      badgeTone="danger"
                      onselect={() => revealDomain(d, `linkTypes[${i}]`)}
                      onopen={() => revealDomain(d, `linkTypes[${i}]`)}
                    />
                  {:else}
                    <p class="empty pad3">No link types.</p>
                  {/each}
                {/if}
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
