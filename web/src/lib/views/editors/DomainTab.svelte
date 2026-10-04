<script lang="ts">
  // Domain tab: general data, graph and issues of a domain; its node types, link types, lifecycles and
  // algorithms have a tab each. Node types and link types are shared by the methodologies that
  // reference the domain. Saved and published on their own: no change,
  // impact or proposal is involved.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import OntologyGraph from '../../components/OntologyGraph.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import { provideActions, useReveal, notify, revealState } from '../../shell/workbench.svelte';
  import { replaceTab, openTab } from '../../shell/tabs.svelte';
  import { domainDrafts, getDomainDraft } from '../../stores/domains.svelte';
  import { formatDate } from '../../api';
    import { domainActions, domainDraftOf, domainSpec, revealDomainPath } from './domainTabs';
  import { methodologySpec } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  // The component is recreated for each tab: the draft is resolved once.
  const d = untrack(() => domainDraftOf(tab));
  const f = $derived(d.form);
  let root = $state<HTMLElement>();
  let pane = $state(untrack(() => tab.params.pane) || 'overview');
  $effect(() => {
    tab.params.pane = pane;
  });

  /** a double-click in the graph opens the editor of the type */
  function openInForm(kind: 'node' | 'link', index: number) {
    revealDomainPath(d.name, d.version, kind === 'node' ? `nodeTypes[${index}]` : `linkTypes[${index}]`, true);
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

  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview', badge: d.usage.length || undefined },
    { id: 'graph', label: 'Graph' },
    { id: 'issues', label: 'Issues', badge: d.allIssues.length || undefined },
  ]);

  // a reveal request for a field of the domain itself (not of a type) shows the overview
  $effect(() => {
    void revealState.seq;
    if (revealState.tabId === tab.id && revealState.path) pane = 'overview';
  });

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
    {#if d.remote}
  <div class="alert" role="alert">
    {d.remote.actor || 'Someone'} {d.remote.type.endsWith('deleted') ? 'removed' : d.remote.type.endsWith('published') ? 'published' : 'saved'} this version
    while you were editing.
    <button type="button" class="small" onclick={() => d.acceptRemote()}>Take theirs</button>
    <button type="button" class="ghost small" onclick={() => d.keepMine()}>Keep mine</button>
  </div>
{/if}
{#if d.error}<div class="alert">{d.error}</div>{/if}
    {#if d.readonly}
      <div class="alert info">
        {d.builtin
          ? `Built-in domain: the platform reads the "${d.name}" namespace in its own way; it is initialised at startup and changes with the platform code (read-only).`
          : d.status === 'published'
            ? 'Published version: it is immutable. Create a new version to modify it.'
            : 'Archived version: read-only.'}
      </div>
    {/if}

    {#if d.isNew}
      <fieldset class="plain" disabled={d.readonly}>
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
      </fieldset>
    {:else}
      <EditorPanes {panes} bind:active={pane} label="Domain sections">
        {#snippet children(active)}
          {#if active === 'graph'}
            <OntologyGraph nodeTypes={f.nodeTypes} linkTypes={f.linkTypes} onopen={openInForm} />
          {:else}
            <fieldset class="plain" disabled={d.readonly}>
              {#if active === 'overview'}
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
              {:else if active === 'issues'}
                <section class="card">
                  <h3>Issues ({d.allIssues.length})</h3>
                  {#if d.allIssues.length}
                    <ul class="plain-list">
                      {#each d.allIssues as i}<li><code>{i.norm || 'domain'}</code>: {i.message}</li>{/each}
                    </ul>
                  {:else}
                    <p class="empty">No issue detected. Use Validate to check the draft.</p>
                  {/if}
                </section>
              {/if}
            </fieldset>
          {/if}
        {/snippet}
      </EditorPanes>
    {/if}
  {/if}
</div>

<style>
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
</style>
