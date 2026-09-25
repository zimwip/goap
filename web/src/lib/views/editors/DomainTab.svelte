<script lang="ts">
  // Domain tab: node types and link types shared by the methodologies that
  // reference the domain. Saved and published on their own: no change,
  // impact or proposal is involved.
  import { untrack, tick } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import RowTools from '../../components/RowTools.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import OntologyGraph from '../../components/OntologyGraph.svelte';
  import LifecycleEditor from '../../components/LifecycleEditor.svelte';
  import NodeTypeMeta from '../../components/NodeTypeMeta.svelte';
  import { provideActions, useReveal, notify, requestReveal } from '../../shell/workbench.svelte';
  import { replaceTab, openTab } from '../../shell/tabs.svelte';
  import { domainDrafts, getDomainDraft } from '../../stores/domains.svelte';
  import { formatDate } from '../../api';
  import { emptyNodeType, emptyLinkType, moveItem, defaultLifecycle } from '../../methodologyForm';
  import { domainActions, domainDraftOf, domainSpec } from './domainTabs';
  import { methodologySpec } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  // The component is recreated for each tab: the draft is resolved once.
  const d = untrack(() => domainDraftOf(tab));
  const f = $derived(d.form);
  let root = $state<HTMLElement>();
  let view = $state<'form' | 'graph'>('form');

  async function openInForm(kind: 'node' | 'link', index: number) {
    view = 'form';
    await tick();
    requestReveal(tab.id, kind === 'node' ? `nodeTypes[${index}]` : `linkTypes[${index}]`);
  }

  const lifecycleNames = $derived(f.lifecycles.map((l) => l.name.trim()).filter(Boolean));

  /** nearest ancestor that names a lifecycle, when the type names none itself */
  function inheritedLifecycle(i: number): { type: string; lifecycle: string } | undefined {
    if (f.nodeTypes[i].lifecycle) return undefined;
    const seen = new Set<string>();
    let cur = f.nodeTypes[i].extends;
    while (cur && !seen.has(cur)) {
      seen.add(cur);
      const p = f.nodeTypes.find((t) => t.name.trim() === cur);
      if (!p) return undefined;
      if (p.lifecycle) return { type: p.name, lifecycle: p.lifecycle };
      cur = p.extends;
    }
    return undefined;
  }

  /** does a node type using the lifecycle embed other nodes (a document)? */
  const usedByDocument = (name: string) => f.nodeTypes.some((t) => t.document.trim() && t.lifecycle === name);

  function addLifecycle() {
    let name = 'lifecycle';
    for (let k = 2; f.lifecycles.some((l) => l.name === name); k++) name = `lifecycle-${k}`;
    f.lifecycles.push(defaultLifecycle(name));
  }

  async function createNew() {
    const saved = await d.save();
    if (!saved) {
      if (d.error) notify(d.error, 'error');
      return;
    }
    const name = saved.name || d.form.name.trim();
    const version = saved.version || d.form.version.trim();
    domainDrafts.delete('new');
    getDomainDraft(name, version);
    replaceTab(tab.id, domainSpec(name, version));
    notify(`Domain ${name} v${version} created.`, 'ok');
  }

  provideActions(
    () => tab.id,
    () =>
      d.isNew
        ? [
            {
              id: 'save',
              label: d.busy === 'save' ? 'Creating…' : 'Create draft',
              icon: 'save',
              primary: true,
              shortcut: 'Ctrl+S',
              disabled: !!d.busy || !d.form.name.trim() || !d.form.version.trim(),
              run: createNew,
            },
          ]
        : domainActions(d),
  );
  useReveal(
    () => tab.id,
    () => root,
  );
</script>

<div class="editor-page" bind:this={root}>
  {#if d.loading}
    <p class="empty">Loading…</p>
  {:else if d.loadError}
    <div class="alert">{d.loadError}</div>
  {:else}
    <div class="editor-head">
      <Icon name="graph" size={18} />
      <h2>{d.isNew ? 'New domain' : d.label}</h2>
      <StatusBadge status={d.status} />
      {#if d.dirty}<span class="dirty" title="Unsaved changes">● modified</span>{/if}
    </div>
    {#if d.error}<div class="alert">{d.error}</div>{/if}
    {#if d.readonly}
      <div class="alert info">
        {d.status === 'published'
          ? 'Published version: it is immutable. Create a new version to modify it.'
          : 'Archived version: read-only.'}
      </div>
    {/if}

    {#if !d.isNew}
      <div class="seg" role="tablist" aria-label="Domain view">
        <button type="button" role="tab" aria-selected={view === 'form'} class:on={view === 'form'} onclick={() => (view = 'form')}>Definition</button>
        <button type="button" role="tab" aria-selected={view === 'graph'} class:on={view === 'graph'} onclick={() => (view = 'graph')}>
          <Icon name="graph" size={13} /> Graph
        </button>
      </div>
    {/if}

    {#if view === 'graph' && !d.isNew}
      <OntologyGraph nodeTypes={f.nodeTypes} linkTypes={f.linkTypes} onopen={openInForm} />
    {/if}

    <fieldset class="plain" disabled={d.readonly} hidden={view === 'graph' && !d.isNew}>
      <section class="card" id="d-general">
        <h3>General</h3>
        <div class="grid">
          <div class="field">
            <label for="d-name">Name</label>
            <input id="d-name" type="text" class="mono" bind:value={f.name} disabled={!d.isNew} class:bad={d.bad('name')} data-path="name" placeholder="alm" />
          </div>
          <div class="field">
            <label for="d-version">Version</label>
            <input id="d-version" type="text" class="mono" bind:value={f.version} disabled={!d.isNew} class:bad={d.bad('version')} data-path="version" placeholder="0.1.0" />
          </div>
        </div>
        <div class="field">
          <label for="d-desc">Description</label>
          <textarea id="d-desc" rows="2" bind:value={f.description} data-path="description"></textarea>
        </div>
        {#if d.meta.updatedAt || d.meta.publishedAt}
          <p class="hint">
            {#if d.meta.createdAt}Created on {formatDate(d.meta.createdAt)}.{/if}
            {#if d.meta.updatedAt}Modified on {formatDate(d.meta.updatedAt)}{d.meta.updatedBy ? ` by ${d.meta.updatedBy}` : ''}.{/if}
            {#if d.meta.publishedAt}Published on {formatDate(d.meta.publishedAt)}.{/if}
          </p>
        {/if}
        {#if d.isNew}<p class="hint">Create the draft to add node types and link types.</p>{/if}
      </section>

      {#if !d.isNew}
        <section class="card" id="d-types">
          <h3>Node types</h3>
          {#each f.nodeTypes as n, i}
            <div class="item nt" class:has-issues={d.count(`nodeTypes[${i}]`) > 0} data-path="nodeTypes[{i}]">
              <input type="text" class="mono" aria-label="Node type name" bind:value={n.name} class:bad={d.bad(`nodeTypes[${i}].name`)} data-path="nodeTypes[{i}].name" placeholder="Requirement" />
              <select aria-label="Parent type (extends)" title="Parent type: the subtype inherits its properties and the link types that accept it" bind:value={n.extends} class:bad={d.bad(`nodeTypes[${i}].extends`)} data-path="nodeTypes[{i}].extends">
                <option value="">— no parent —</option>
                {#if n.extends && !d.nodeTypeNames.includes(n.extends)}<option value={n.extends}>{n.extends} (unknown)</option>{/if}
                {#each d.nodeTypeNames.filter((t) => t !== n.name.trim()) as t (t)}<option value={t}>extends {t}</option>{/each}
              </select>
              <input type="text" aria-label="Description" bind:value={n.description} placeholder="Description" />
              <input type="text" class="mono" aria-label="Properties (comma-separated)" bind:value={n.properties} placeholder="title, description" />
              {#if !d.readonly}
                <RowTools index={i} count={f.nodeTypes.length} label="the node type" onmove={(delta) => moveItem(f.nodeTypes, i, delta)} onremove={() => f.nodeTypes.splice(i, 1)} />
              {/if}
            </div>
            <NodeTypeMeta
              bind:n={f.nodeTypes[i]}
              lifecycles={lifecycleNames}
              typeNames={d.nodeTypeNames}
              inherited={inheritedLifecycle(i)}
              readonly={d.readonly}
              path="nodeTypes[{i}]"
            />
          {:else}
            <p class="empty">No node types.</p>
          {/each}
          {#if !d.readonly}
            <button type="button" class="small" onclick={() => f.nodeTypes.push(emptyNodeType())}>+ Node type</button>
          {/if}
          <p class="hint cols">
            Columns: name · parent type · description · properties (comma-separated). A subtype inherits its parent's
            properties and link types; conditions on the parent apply to it as well (<code>x.types</code> contains all
            supertypes).
          </p>

          <h3 class="sub" data-path="linkTypes">Link types</h3>
          {#each f.linkTypes as l, i}
            <div class="item" class:has-issues={d.count(`linkTypes[${i}]`) > 0} data-path="linkTypes[{i}]">
              <input type="text" class="mono" aria-label="Link type name" bind:value={l.name} class:bad={d.bad(`linkTypes[${i}].name`)} data-path="linkTypes[{i}].name" placeholder="verifies" />
              <select aria-label="From" bind:value={l.from} class:bad={d.bad(`linkTypes[${i}].from`)} data-path="linkTypes[{i}].from">
                <option value="">— from —</option>
                {#if l.from && !d.nodeTypeNames.includes(l.from)}<option value={l.from}>{l.from} (unknown)</option>{/if}
                {#each d.nodeTypeNames as t (t)}<option value={t}>{t}</option>{/each}
              </select>
              <select aria-label="To" bind:value={l.to} class:bad={d.bad(`linkTypes[${i}].to`)} data-path="linkTypes[{i}].to">
                <option value="">— to —</option>
                {#if l.to && !d.nodeTypeNames.includes(l.to)}<option value={l.to}>{l.to} (unknown)</option>{/if}
                {#each d.nodeTypeNames as t (t)}<option value={t}>{t}</option>{/each}
              </select>
              {#if !d.readonly}
                <RowTools index={i} count={f.linkTypes.length} label="the link type" onmove={(delta) => moveItem(f.linkTypes, i, delta)} onremove={() => f.linkTypes.splice(i, 1)} />
              {/if}
            </div>
          {:else}
            <p class="empty">No link types.</p>
          {/each}
          {#if !d.readonly}
            <button type="button" class="small" onclick={() => f.linkTypes.push(emptyLinkType())}>+ Link type</button>
          {/if}
        </section>

        <section class="card" id="d-lifecycles">
          <h3>Lifecycles</h3>
          <p class="hint">
            A lifecycle gives the nodes of a type a state. A node is modified only in an <em>editable</em> state, which it holds only
            through a change: reopen it, edit it, and move it to a non-editable state before the change is applied. Node types name
            their lifecycle above; a subtype inherits it.
          </p>
          {#each f.lifecycles as l, i}
            <details class="lifecycle" open={f.lifecycles.length === 1} data-path="lifecycles[{i}]">
              <summary class:has-issues={d.count(`lifecycles[${i}]`) > 0}>
                <strong class="mono">{l.name || '(unnamed)'}</strong>
                <span class="hint">{l.states.length} states · {l.transitions.length} transitions · used by {f.nodeTypes.filter((t) => t.lifecycle === l.name).length} type(s)</span>
              </summary>
              <div class="lifecycle-body">
                <div class="field">
                  <label for="lc-name-{i}">Name</label>
                  <input id="lc-name-{i}" type="text" class="mono" bind:value={l.name} class:bad={d.bad(`lifecycles[${i}].name`)} data-path="lifecycles[{i}].name" placeholder="requirement" />
                </div>
                <LifecycleEditor bind:lc={f.lifecycles[i]} readonly={d.readonly} documents={usedByDocument(l.name)} path="lifecycles[{i}]" />
                {#if !d.readonly}
                  <button type="button" class="small danger" onclick={() => f.lifecycles.splice(i, 1)}>Delete the lifecycle</button>
                {/if}
              </div>
            </details>
          {:else}
            <p class="empty">No lifecycles: the nodes have no state.</p>
          {/each}
          {#if !d.readonly}
            <button type="button" class="small" onclick={addLifecycle}>+ Lifecycle</button>
          {/if}
        </section>

        {#if d.allIssues.length}
          <section class="card">
            <h3>Issues ({d.allIssues.length})</h3>
            <ul class="plain-list">
              {#each d.allIssues as i}<li><code>{i.norm || 'domain'}</code>: {i.message}</li>{/each}
            </ul>
          </section>
        {/if}

        <section class="card" id="d-usage">
          <h3>Used by</h3>
          {#if d.usage.length}
            <ul class="plain-list">
              {#each d.usage as u}
                <li>
                  <button type="button" class="link" onclick={() => openTab(methodologySpec(u.name ?? '', u.version ?? ''))}>{u.name} v{u.version}</button>
                  <span class="hint">{u.status}</span>
                </li>
              {/each}
            </ul>
            <p class="hint">A version cannot be deleted or archived while a methodology is pinned to it, and a new version is refused if it would break a published methodology following the latest version.</p>
          {:else}
            <p class="empty">No methodology references this version.</p>
          {/if}
        </section>
      {/if}
    </fieldset>
  {/if}
</div>

<style>
  .seg {
    display: inline-flex;
    margin: 0.2rem 0 0.7rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .seg button {
    border: none;
    border-radius: 0;
    background: var(--surface);
    font-weight: 500;
    padding: 0.3rem 0.9rem;
  }
  .seg button.on {
    background: var(--accent);
    color: var(--accent-text);
  }
  .lifecycle {
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.4rem 0.7rem;
    margin-bottom: 0.5rem;
  }
  .lifecycle summary {
    cursor: pointer;
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
  }
  .lifecycle summary.has-issues {
    box-shadow: inset 3px 0 0 var(--danger);
    padding-left: 5px;
  }
  .lifecycle-body {
    display: grid;
    gap: 0.5rem;
    padding: 0.5rem 0 0.2rem;
  }
  h3.sub {
    margin-top: 1rem;
  }
  .item {
    display: grid;
    grid-template-columns: minmax(120px, 1fr) minmax(140px, 1.5fr) minmax(140px, 1.3fr) auto;
    gap: 0.4rem;
    align-items: center;
    margin-bottom: 0.3rem;
    border-radius: var(--radius-sm);
  }
  .item.nt {
    grid-template-columns: minmax(120px, 1fr) minmax(110px, 0.9fr) minmax(140px, 1.5fr) minmax(140px, 1.3fr) auto;
  }
  .item.has-issues {
    box-shadow: inset 3px 0 0 var(--danger);
    padding-left: 5px;
  }
  .cols {
    margin: 0.3rem 0 0;
  }
  .dirty {
    color: var(--warn);
    font-size: 0.85rem;
    font-weight: 600;
  }
  .plain-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.2rem;
  }
  @media (max-width: 800px) {
    .item,
    .item.nt {
      grid-template-columns: 1fr;
    }
  }
</style>
