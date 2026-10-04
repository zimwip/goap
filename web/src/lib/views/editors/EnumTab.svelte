<script lang="ts">
  // "Enum" tab: a closed list of values of a domain that enum attributes refer to, edited in the draft of its domain.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import RowTools from '../../components/RowTools.svelte';
  import { provideActions, useReveal, notify } from '../../shell/workbench.svelte';
  import { closeWhere } from '../../shell/tabs.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { moveItem } from '../../methodologyForm';
  import { algorithmToolbar, domainDraftOf, enumIndex } from './domainTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => domainDraftOf(tab));
  let root = $state<HTMLElement>();
  const f = $derived(d.form);
  const index = $derived(enumIndex(d, tab));
  const x = $derived(index >= 0 ? f.enums[index] : undefined);
  const path = $derived(`enums[${index}]`);
  const issues = $derived(d.allIssues.filter((i) => i.norm === path || i.norm.startsWith(`${path}.`) || i.norm.startsWith(`${path}[`)));
  /** the attributes that use the enum */
  const users = $derived(
    x && x.name
      ? [
          ...f.nodeTypes.flatMap((n) => n.attributes.filter((a) => a.enum === x.name).map((a) => `${n.name || '(unnamed)'}.${a.name || '(unnamed)'}`)),
          ...f.linkTypes.flatMap((l) => l.attributes.filter((a) => a.enum === x.name).map((a) => `${l.name || '(unnamed)'}.${a.name || '(unnamed)'}`)),
        ]
      : [],
  );

  $effect(() => {
    if (!x) return;
    tab.params.uid = x.uid;
    tab.params.en = x.name;
  });

  // renaming an enum renames the references of its attributes
  let prev = { uid: '', name: '' };
  $effect(() => {
    const cur = x ? { uid: x.uid, name: x.name } : { uid: '', name: '' };
    const before = untrack(() => prev);
    prev = cur;
    if (!cur.uid || before.uid !== cur.uid || !before.name || !cur.name || before.name === cur.name) return;
    untrack(() => {
      for (const n of f.nodeTypes) for (const a of n.attributes) if (a.enum === before.name) a.enum = cur.name;
      for (const l of f.linkTypes) for (const a of l.attributes) if (a.enum === before.name) a.enum = cur.name;
    });
  });

  async function remove() {
    if (!x) return;
    if (users.length) return void notify(`${users.length} attribute(s) use ${x.name}: ${users.slice(0, 3).join(', ')}${users.length > 3 ? '…' : ''}. Change them first.`, 'error');
    if (!(await confirmDialog({ message: `Remove the enum "${x.name || 'unnamed'}" from the draft?`, danger: true }))) return;
    const uid = x.uid;
    closeWhere((t) => t.kind === 'enum' && t.params.uid === uid);
    f.enums.splice(index, 1);
  }

  provideActions(
    () => tab.id,
    () => algorithmToolbar(d, { label: 'Delete enum', run: remove }),
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
    <div class="alert warn">Enum not found in the draft {d.label} (deleted or renamed?).</div>
  {:else}
    <div class="editor-head">
      <Icon name="tag" size={18} />
      <h2>{x.name || '(unnamed)'}</h2>
      <span class="hint">enum · {d.label}</span>
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
            <label for="en-name">Name</label>
            <input id="en-name" type="text" class="mono" bind:value={x.name} class:bad={d.bad(`${path}.name`)} data-path="{path}.name" placeholder="priority" />
          </div>
          <div class="field">
            <label for="en-desc">Description</label>
            <input id="en-desc" type="text" bind:value={x.description} />
          </div>
        </div>
        <p class="hint">{users.length ? `Used by ${users.join(', ')}.` : 'No attribute uses this enum yet.'}</p>
      </section>
      <section class="card" data-path="{path}.values">
        <h3>Values</h3>
        {#each x.values as v, i}
          <div class="vrow" data-path="{path}.values[{i}]">
            <input type="text" class="mono" aria-label="Value" bind:value={v.value} class:bad={d.bad(`${path}.values[${i}].value`)} data-path="{path}.values[{i}].value" placeholder="high" />
            <input type="text" aria-label="Label" bind:value={v.label} placeholder="Label (defaults to the value)" />
            {#if !d.readonly}
              <RowTools index={i} count={x.values.length} label="the value" onmove={(delta) => moveItem(x.values, i, delta)} onremove={() => x.values.splice(i, 1)} />
            {/if}
          </div>
        {:else}
          <p class="empty">No values.</p>
        {/each}
        {#if !d.readonly}<button type="button" class="small" onclick={() => x.values.push({ value: '', label: '' })}>+ Value</button>{/if}
        <p class="hint">The order is the order of the dropdown.</p>
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
  .vrow {
    display: grid;
    grid-template-columns: minmax(120px, 1fr) minmax(140px, 1.5fr) auto;
    gap: 0.4rem;
    align-items: center;
    margin-bottom: 0.3rem;
  }
</style>
