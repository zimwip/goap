<script lang="ts">
  // "Item type" tab (ADR 0128): a type of change item a domain declares, edited in the draft of its domain. What the
  // platform code adds to it (a validation, a permission) is read from the graph's list of the types in force.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import AttributeEditor from '../../components/AttributeEditor.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { closeWhere } from '../../shell/tabs.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { itemTypes, refreshItemTypes, itemTypeOf } from '../../stores/itemTypes.svelte';
  import { itemTypeCodeLabel, itemTypePermissionLabel, itemTypeShownIn } from '../../itemTypes';
  import { algorithmToolbar, domainDraftOf, itemTypeIndex } from './domainTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => domainDraftOf(tab));
  let root = $state<HTMLElement>();
  const f = $derived(d.form);
  const index = $derived(itemTypeIndex(d, tab));
  const x = $derived(index >= 0 ? f.itemTypes[index] : undefined);
  const path = $derived(`itemTypes[${index}]`);
  const issues = $derived(d.allIssues.filter((i) => i.norm === path || i.norm.startsWith(`${path}.`) || i.norm.startsWith(`${path}[`)));
  const enumNames = $derived(f.enums.map((e) => e.name.trim()).filter(Boolean));
  /** the type in force, when this version of the domain is the one in force */
  const inForce = $derived.by(() => {
    const t = x ? itemTypeOf(x.name.trim()) : undefined;
    return t && t.domain === d.name && t.domainVersion === d.version ? t : undefined;
  });

  $effect(() => {
    void refreshItemTypes();
  });

  $effect(() => {
    if (!x) return;
    tab.params.uid = x.uid;
    tab.params.it = x.name;
  });

  async function remove() {
    if (!x) return;
    if (!(await confirmDialog({ message: `Remove the item type "${x.name || 'unnamed'}" from the draft? Changes will no longer accept new items of it once the version is published.`, danger: true }))) return;
    const uid = x.uid;
    closeWhere((t) => t.kind === 'itemtype' && t.params.uid === uid);
    f.itemTypes.splice(index, 1);
  }

  provideActions(
    () => tab.id,
    () => algorithmToolbar(d, { label: 'Delete item type', run: remove }),
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
  {:else if !x}
    <div class="alert warn">Item type not found in the draft {d.label} (deleted or renamed?).</div>
  {:else}
    <div class="editor-head">
      <Icon name="tag" size={18} />
      <h2 class="mono">{x.name || '(unnamed)'}</h2>
      <span class="hint">item type · {d.label}</span>
      <StatusBadge status={d.status} />
      {#if d.dirty}<span class="dirty" title="Unsaved changes">● modified</span>{/if}
    </div>
    {#if d.error}<div class="alert">{d.error}</div>{/if}
    {#if d.readonly}
      <div class="alert info">
        {d.builtin
          ? 'Built-in domain: it changes with the platform code.'
          : d.status === 'published'
            ? 'Published version: it is immutable. Create a new version of the domain to modify it.'
            : 'Archived version: read-only.'}
      </div>
    {/if}
    {#if issues.length}
      <div class="alert warn">
        <ul class="plain-list">{#each issues as i}<li><code>{i.norm}</code>: {i.message}</li>{/each}</ul>
      </div>
    {/if}

    <fieldset class="plain" disabled={d.readonly}>
      <section class="card" data-path={path}>
        <h3>General</h3>
        <div class="grid">
          <div class="field">
            <label for="it-name">Name</label>
            <input id="it-name" type="text" class="mono" bind:value={x.name} class:bad={d.bad(`${path}.name`)} data-path="{path}.name" placeholder="testResult" />
          </div>
          <div class="field">
            <label for="it-desc">Description</label>
            <input id="it-desc" type="text" bind:value={x.description} data-path="{path}.description" />
          </div>
        </div>
        <p class="hint">
          A change holds items next to its impacts: each names its item type in <code>type</code> and, within it, what it is about in
          <code>target</code>. The name is one across the domains in force; the graph refuses an item of a type no domain declares.
        </p>
      </section>
    </fieldset>
    <section class="card">
      <h3>Data</h3>
      <p class="hint">The keys of the data of its items. The graph checks the type and enum of the keys an item carries; other keys stay free.</p>
      <AttributeEditor bind:attrs={f.itemTypes[index].attributes} enums={enumNames} validatorInstances={[]} node={false} bad={(p) => d.bad(p)} readonly={d.readonly} {path} />
    </section>
    <section class="card">
      <h3>In the platform</h3>
      {#if !itemTypes.loaded}
        <p class="empty">Loading…</p>
      {:else if itemTypes.error}
        <div class="alert small">{itemTypes.error}</div>
      {:else}
        <dl class="props">
          <dt>Code</dt>
          <dd>{itemTypeCodeLabel(inForce)}</dd>
          <dt>Writing one asks</dt>
          <dd>{itemTypePermissionLabel(inForce)}</dd>
          <dt>Shown in</dt>
          <dd>{itemTypeShownIn(x.name.trim())}</dd>
        </dl>
      {/if}
    </section>
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
  .props {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.35rem 1rem;
    margin: 0;
  }
  .props dt {
    color: var(--muted);
  }
  .props dd {
    margin: 0;
  }
</style>
