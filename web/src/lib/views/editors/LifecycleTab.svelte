<script lang="ts">
  // "Lifecycle" tab: states and transitions of one lifecycle of a domain, edited in the draft of its domain.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import LifecycleEditor from '../../components/LifecycleEditor.svelte';
  import { provideActions, useReveal, notify } from '../../shell/workbench.svelte';
  import { closeWhere } from '../../shell/tabs.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { algorithmToolbar, domainDraftOf, lifecycleIndex } from './domainTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => domainDraftOf(tab));
  let root = $state<HTMLElement>();

  const f = $derived(d.form);
  const index = $derived(lifecycleIndex(d, tab));
  const x = $derived(index >= 0 ? f.lifecycles[index] : undefined);
  const path = $derived(`lifecycles[${index}]`);
  const issues = $derived(d.allIssues.filter((i) => i.norm === path || i.norm.startsWith(`${path}.`) || i.norm.startsWith(`${path}[`)));
  const users = $derived(x && x.name ? f.nodeTypes.filter((t) => t.lifecycle === x.name).length : 0);

  $effect(() => {
    if (!x) return;
    tab.params.uid = x.uid;
    tab.params.lc = x.name;
  });

  /** names of the algorithm instances of a usage */
  const instancesOf = (usage: string) =>
    f.instances.filter((i) => i.name.trim() && f.algorithms.find((a) => a.name === i.algorithm)?.type === usage).map((i) => i.name.trim());

  /** does a node type using the lifecycle embed other nodes (a document)? */
  const usedByDocument = $derived(!!x && f.nodeTypes.some((t) => t.document.trim() && t.lifecycle === x.name));

  async function remove() {
    if (!x) return;
    if (users) return void notify(`${users} node type(s) use ${x.name}: change their lifecycle first.`, 'error');
    if (!(await confirmDialog({ message: `Remove the lifecycle "${x.name || 'unnamed'}" from the draft?`, danger: true }))) return;
    const uid = x.uid;
    closeWhere((t) => t.kind === 'lifecycle' && t.params.uid === uid);
    f.lifecycles.splice(index, 1);
  }

  provideActions(
    () => tab.id,
    () => algorithmToolbar(d, { label: 'Delete lifecycle', run: remove }),
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
    <div class="alert warn">Lifecycle not found in the draft {d.label} (deleted or renamed?).</div>
  {:else}
    <div class="editor-head">
      <Icon name="runs" size={18} />
      <h2>{x.name || '(unnamed)'}</h2>
      <span class="hint">lifecycle · {d.label}</span>
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
        <p class="hint">
          A lifecycle gives the nodes of a type a state. A node is modified only in an <em>editable</em> state, which it holds only
          through a change: reopen it, edit it, and move it to a non-editable state before the change is applied. Node types name
          their lifecycle; a subtype inherits it. Used by {users} type(s).
        </p>
        <div class="field">
          <label for="lc-name">Name</label>
          <input id="lc-name" type="text" class="mono" bind:value={x.name} class:bad={d.bad(`${path}.name`)} data-path="{path}.name" placeholder="requirement" />
        </div>
        <div class="field">
          <label for="lc-desc">Description</label>
          <input id="lc-desc" type="text" bind:value={x.description} />
        </div>
        <label class="inline" title="A node may stay in an editable state (a long-lived status) when a change is applied (ADR 0048)"><input type="checkbox" bind:checked={x.restInEditable} /> nodes may rest in an editable state</label>
        <LifecycleEditor bind:lc={f.lifecycles[index]} readonly={d.readonly} guardInstances={instancesOf('transition_guard')} actionInstances={instancesOf('transition_action')} bad={(p) => d.bad(p)} documents={usedByDocument} {path} />
      </section>
    </fieldset>
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
