<script lang="ts">
  // "Node type" tab: one node type of a domain, edited in the draft of its domain.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import ValidatorList from '../../components/ValidatorList.svelte';
  import NodeTypeMeta from '../../components/NodeTypeMeta.svelte';
  import AttributeEditor from '../../components/AttributeEditor.svelte';
  import FormDesigner from '../../forms/designer/FormDesigner.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { closeWhere } from '../../shell/tabs.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { linksOf } from './typeLinks';
  import { algorithmToolbar, domainDraftOf, nodeTypeIndex, openLifecycle, openLinkType } from './domainTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => domainDraftOf(tab));
  let root = $state<HTMLElement>();

  const f = $derived(d.form);
  const index = $derived(nodeTypeIndex(d, tab));
  const x = $derived(index >= 0 ? f.nodeTypes[index] : undefined);
  const path = $derived(`nodeTypes[${index}]`);
  const issues = $derived(d.allIssues.filter((i) => i.norm === path || i.norm.startsWith(`${path}.`) || i.norm.startsWith(`${path}[`)));

  // keep the tab bound to the node type when it is renamed or the draft is reloaded
  $effect(() => {
    if (!x) return;
    tab.params.uid = x.uid;
    tab.params.nt = x.name;
  });

  const enumNames = $derived(f.enums.map((e) => e.name.trim()).filter(Boolean));
  const lifecycleNames = $derived(f.lifecycles.map((l) => l.name.trim()).filter(Boolean));

  /** nearest ancestor that names a lifecycle, when the type names none itself */
  const inherited = $derived.by(() => {
    if (!x || x.lifecycle) return undefined;
    const seen = new Set<string>();
    let cur = x.extends;
    while (cur && !seen.has(cur)) {
      seen.add(cur);
      const p = f.nodeTypes.find((t) => t.name.trim() === cur);
      if (!p) return undefined;
      if (p.lifecycle) return { type: p.name, lifecycle: p.lifecycle };
      cur = p.extends;
    }
    return undefined;
  });

  /** names of the attributes the type inherits */
  const inheritedNames = $derived.by(() => {
    const out = new Set<string>();
    const seen = new Set<string>(x ? [x.name.trim()] : []);
    let cur = x?.extends;
    while (cur && !seen.has(cur)) {
      seen.add(cur);
      const p = f.nodeTypes.find((t) => t.name.trim() === cur);
      if (!p) break;
      for (const a of p.attributes) if (a.name.trim()) out.add(a.name.trim());
      cur = p.extends;
    }
    return [...out];
  });

  /** names of the algorithm instances of a usage */
  const instancesOf = (usage: string) =>
    f.instances.filter((i) => i.name.trim() && f.algorithms.find((a) => a.name === i.algorithm)?.type === usage).map((i) => i.name.trim());

  const outgoing = $derived(x ? linksOf(f.nodeTypes, f.linkTypes, x.name, 'from') : []);
  const ingoing = $derived(x ? linksOf(f.nodeTypes, f.linkTypes, x.name, 'to') : []);

  function openLifecycleNamed(name: string) {
    const i = f.lifecycles.findIndex((l) => l.name === name);
    if (i >= 0) openLifecycle(d, i);
  }

  async function remove() {
    if (!x) return;
    if (!(await confirmDialog({ message: `Remove the node type "${x.name || 'unnamed'}" from the draft?`, danger: true }))) return;
    const uid = x.uid;
    closeWhere((t) => t.kind === 'nodetype' && t.params.uid === uid);
    f.nodeTypes.splice(index, 1);
  }

  provideActions(
    () => tab.id,
    () => algorithmToolbar(d, { label: 'Delete node type', run: remove }),
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
    <div class="alert warn">Node type not found in the draft {d.label} (deleted or renamed?).</div>
  {:else}
    <div class="editor-head">
      <Icon name="node" size={18} />
      <h2>{x.name || '(unnamed)'}</h2>
      <span class="hint">node type · {d.label}</span>
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
            <label for="nt-name">Name</label>
            <input id="nt-name" type="text" class="mono" bind:value={x.name} class:bad={d.bad(`${path}.name`)} data-path="{path}.name" placeholder="Requirement" />
          </div>
          <div class="field">
            <label for="nt-ext">Parent type <span class="hint">(extends)</span></label>
            <select id="nt-ext" title="The subtype inherits its properties and the link types that accept it" bind:value={x.extends} class:bad={d.bad(`${path}.extends`)} data-path="{path}.extends">
              <option value="">— no parent —</option>
              {#if x.extends && !d.typeOptions.includes(x.extends)}<option value={x.extends}>{x.extends} (unknown)</option>{/if}
              {#each d.typeOptions.filter((t) => t !== x.name.trim()) as t (t)}<option value={t}>{t}</option>{/each}
            </select>
          </div>
        </div>
        <div class="field">
          <label for="nt-desc">Description</label>
          <input id="nt-desc" type="text" bind:value={x.description} />
        </div>
        <p class="hint">
          A subtype inherits its parent's properties and link types; conditions on the parent apply to it as well
          (<code>x.types</code> contains all supertypes). An attribute of the same name overrides the inherited one; validators accumulate.
        </p>
</section>
</fieldset>
      <section class="card">
        <h3>Attributes</h3>
        <p class="hint">What the nodes carry, and what the interface needs to display and edit them. Each attribute holds its own validators.</p>
        <AttributeEditor bind:attrs={f.nodeTypes[index].attributes} enums={enumNames} validatorInstances={instancesOf('property_validator')} inherited={inheritedNames} bad={(p) => d.bad(p)} readonly={d.readonly} {path} />
      </section>
      <section class="card" data-path="{path}.validators">
        <h3>Validators</h3>
        <p class="hint">Node validators check a node as a whole, across its attributes, when the draft is accepted and at landing, after the attribute validators and those of the parent types. Run in this order.</p>
        <ValidatorList bind:list={f.nodeTypes[index].validators} instances={instancesOf('node_validator')} usage="node_validator" noun="node validator" bad={(p) => d.bad(p)} readonly={d.readonly} path="{path}.validators" />
      </section>
      <section class="card">
        <h3>Links</h3>
        <p class="hint">The link types that leave or reach this node type, including those of its parent types. A view only: each link type is edited in its own tab.</p>
        <h4>Outgoing</h4>
        {#if outgoing.length}
          <ul class="plain-list">
            {#each outgoing as o (o.index)}
              <li>
                <code>{o.name}</code> → <code>{o.other || '(any)'}</code>
                {#if o.compose}<span class="hint">· composition</span>{/if}
                {#if o.inheritedFrom}<span class="hint">· inherited from {o.inheritedFrom}</span>{/if}
                <button type="button" class="small link" title="Open the link type {o.name}" onclick={() => openLinkType(d, o.index)}>Open ↗</button>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="hint">No link type leaves this node type.</p>
        {/if}
        <h4>Ingoing</h4>
        {#if ingoing.length}
          <ul class="plain-list">
            {#each ingoing as o (o.index)}
              <li>
                <code>{o.other || '(any)'}</code> <span class="hint">via</span> <code>{o.name}</code>
                {#if o.compose}<span class="hint">· composition (this type is a part)</span>{/if}
                {#if o.inheritedFrom}<span class="hint">· inherited from {o.inheritedFrom}</span>{/if}
                <button type="button" class="small link" title="Open the link type {o.name}" onclick={() => openLinkType(d, o.index)}>Open ↗</button>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="hint">No link type reaches this node type.</p>
        {/if}
      </section>
      <section class="card">
        <NodeTypeMeta
          bind:n={f.nodeTypes[index]}
          lifecycles={lifecycleNames}
          typeNames={d.nodeTypeNames}
          {inherited}
          onopenLifecycle={openLifecycleNamed}
          bad={(p) => d.bad(p)}
          readonly={d.readonly}
          {path}
        />
      </section>
      <section class="card">
        <h3>Creation form</h3>
        <p class="hint">
          What a person fills in to create a node of this type (ADR 0115 §7): fields named after its attributes, links starting from the
          node (<code>fromKey: $self</code>) and explanations. None: the creation dialogs ask the key and type only{#if x.extends}, or show the form of
          the parent type{/if}.
        </p>
        <FormDesigner id="nt-form-{x.uid}" bind:value={f.nodeTypes[index].form} disabled={d.readonly} />
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
