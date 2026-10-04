<script lang="ts">
  // "Link type" tab: one link type of a domain, edited in the draft of its domain.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import AttributeEditor from '../../components/AttributeEditor.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { closeWhere } from '../../shell/tabs.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { algorithmToolbar, domainDraftOf, linkTypeIndex } from './domainTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => domainDraftOf(tab));
  let root = $state<HTMLElement>();

  const f = $derived(d.form);
  const index = $derived(linkTypeIndex(d, tab));
  const x = $derived(index >= 0 ? f.linkTypes[index] : undefined);
  const path = $derived(`linkTypes[${index}]`);
  const issues = $derived(d.allIssues.filter((i) => i.norm === path || i.norm.startsWith(`${path}.`) || i.norm.startsWith(`${path}[`)));

  const enumNames = $derived(f.enums.map((e) => e.name.trim()).filter(Boolean));
  const instancesOf = (usage: string) =>
    f.instances.filter((i) => i.name.trim() && f.algorithms.find((a) => a.name === i.algorithm)?.type === usage).map((i) => i.name.trim());

  $effect(() => {
    if (!x) return;
    tab.params.uid = x.uid;
    tab.params.lt = x.name;
  });

  async function remove() {
    if (!x) return;
    if (!(await confirmDialog({ message: `Remove the link type "${x.name || 'unnamed'}" from the draft?`, danger: true }))) return;
    const uid = x.uid;
    closeWhere((t) => t.kind === 'linktype' && t.params.uid === uid);
    f.linkTypes.splice(index, 1);
  }

  provideActions(
    () => tab.id,
    () => algorithmToolbar(d, { label: 'Delete link type', run: remove }),
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
    <div class="alert warn">Link type not found in the draft {d.label} (deleted or renamed?).</div>
  {:else}
    <div class="editor-head">
      <Icon name="trace" size={18} />
      <h2>{x.name || '(unnamed)'}</h2>
      <span class="hint">link type · {d.label}</span>
      <StatusBadge status={d.status} />
      {#if d.dirty}<span class="dirty" title="Unsaved changes">● modified</span>{/if}
    </div>
    {#if d.error}<div class="alert">{d.error}</div>{/if}
    {#if d.readonly}
      <div class="alert info">
        {d.status === 'published' ? 'Published version: it is immutable. Create a new version of the domain to modify it.' : 'Archived version: read-only.'}
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
            <label for="lt-name">Name</label>
            <input id="lt-name" type="text" class="mono" bind:value={x.name} class:bad={d.bad(`${path}.name`)} data-path="{path}.name" placeholder="verifies" />
          </div>
          <div class="field">
            <label for="lt-from">From</label>
            <select id="lt-from" bind:value={x.from} class:bad={d.bad(`${path}.from`)} data-path="{path}.from">
              <option value="">— from —</option>
              {#if x.from && !d.typeOptions.includes(x.from)}<option value={x.from}>{x.from} (unknown)</option>{/if}
              {#each d.typeOptions as t (t)}<option value={t}>{t}</option>{/each}
            </select>
          </div>
          <div class="field">
            <label for="lt-to">To</label>
            <select id="lt-to" bind:value={x.to} class:bad={d.bad(`${path}.to`)} data-path="{path}.to">
              <option value="">— to —</option>
              {#if x.to && !d.typeOptions.includes(x.to)}<option value={x.to}>{x.to} (unknown)</option>{/if}
              {#each d.typeOptions as t (t)}<option value={t}>{t}</option>{/each}
            </select>
          </div>
        </div>
        <div class="field">
          <label for="lt-desc">Description</label>
          <input id="lt-desc" type="text" bind:value={x.description} />
        </div>
        <label class="inline" title="A composition link: its target is a part of its source, shown as a child by the editors"><input type="checkbox" bind:checked={x.compose} data-path="{path}.compose" /> compose</label>
        <p class="hint">A composition link says its target is a part of its source: the editors show it as a child.</p>
</section>
</fieldset>
      <section class="card">
        <h3>Attributes</h3>
        <p class="hint">What a link of this type carries. Their validators are checked by the domain; they are not yet run when a link is written.</p>
        <AttributeEditor bind:attrs={f.linkTypes[index].attributes} enums={enumNames} validatorInstances={instancesOf('property_validator')} node={false} bad={(p) => d.bad(p)} readonly={d.readonly} {path} />
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
</style>
