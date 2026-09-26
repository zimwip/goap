<script lang="ts">
  // "Algorithm instance" tab: sets the parameter values of an algorithm; the instance is what
  // node types and lifecycle transitions plug (ADR 0018).
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import AlgorithmParamValues from '../../components/AlgorithmParamValues.svelte';
  import AlgorithmTryIt from '../../components/AlgorithmTryIt.svelte';
  import { provideActions, useReveal, notify } from '../../shell/workbench.svelte';
  import { closeWhere, openTab } from '../../shell/tabs.svelte';
  import { plugsOf } from '../../algorithmForm';
  import { algorithmSpec, algorithmToolbar, domainDraftOf, instanceIndex } from './domainTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => domainDraftOf(tab));
  const index = $derived(instanceIndex(d, tab));
  const inst = $derived(index >= 0 ? d.form.instances[index] : undefined);
  const algorithm = $derived(inst ? d.form.algorithms.find((x) => x.name === inst.algorithm) : undefined);
  const plugs = $derived(inst && inst.name ? plugsOf(inst.name, d.form.nodeTypes, d.form.lifecycles) : []);
  let root = $state<HTMLElement>();

  $effect(() => {
    if (!inst) return;
    tab.params.uid = inst.uid;
    tab.params.inst = inst.name;
  });

  // renaming an instance renames the plugs that reference it
  let prev = { uid: '', name: '' };
  $effect(() => {
    const cur = inst ? { uid: inst.uid, name: inst.name } : { uid: '', name: '' };
    const before = untrack(() => prev);
    prev = cur;
    if (!cur.uid || before.uid !== cur.uid || !before.name || !cur.name || before.name === cur.name) return;
    untrack(() => {
      for (const n of d.form.nodeTypes) for (const v of n.validators) if (v.instance === before.name) v.instance = cur.name;
      for (const l of d.form.lifecycles)
        for (const t of l.transitions) {
          t.guards = t.guards.map((g) => (g === before.name ? cur.name : g));
          t.actions = t.actions.map((g) => (g === before.name ? cur.name : g));
        }
    });
  });

  const bad = (p: string) => d.bad(`algorithmInstances[${index}]${p}`);
  const issues = $derived(d.allIssues.filter((i) => i.norm === `algorithmInstances[${index}]` || i.norm.startsWith(`algorithmInstances[${index}].`)));

  function remove() {
    if (!inst) return;
    if (plugs.length) {
      notify(`${inst.name} is plugged (${plugs.length}): unplug it first.`, 'error');
      return;
    }
    if (!confirm(`Remove the instance "${inst.name || 'unnamed'}" from the draft?`)) return;
    const uid = inst.uid;
    closeWhere((t) => t.kind === 'instance' && t.params.uid === uid);
    d.form.instances.splice(index, 1);
  }

  provideActions(
    () => tab.id,
    () => algorithmToolbar(d, { label: 'Delete instance', run: remove }),
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
  {:else if !inst}
    <div class="alert warn">Instance not found in the draft {d.label} (deleted or renamed?).</div>
  {:else}
    <div class="editor-head">
      <Icon name="tag" size={18} />
      <h2>{inst.name || '(unnamed)'}</h2>
      <span class="hint">algorithm instance · {d.label}</span>
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
      <div class="alert warn" data-testid="instance-issues">
        <ul class="plain-list">{#each issues as i}<li><code>{i.norm}</code>: {i.message}</li>{/each}</ul>
      </div>
    {/if}

    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <h3>General</h3>
        <div class="grid">
          <div class="field">
            <label for="inst-name">Name</label>
            <input id="inst-name" type="text" class="mono" bind:value={inst.name} class:bad={bad('.name')} data-path="algorithmInstances[{index}].name" placeholder="title-max-200" />
          </div>
          <div class="field">
            <label for="inst-alg">Algorithm</label>
            <select id="inst-alg" bind:value={inst.algorithm} class:bad={bad('.algorithm')} data-path="algorithmInstances[{index}].algorithm">
              <option value="">— choose —</option>
              {#if inst.algorithm && !algorithm}<option value={inst.algorithm}>{inst.algorithm} (unknown)</option>{/if}
              {#each d.form.algorithms as x (x.uid)}<option value={x.name}>{x.name} ({x.type})</option>{/each}
            </select>
          </div>
        </div>
        <div class="field">
          <label for="inst-desc">Description</label>
          <input id="inst-desc" type="text" bind:value={inst.description} />
        </div>
        {#if algorithm}
          <p class="hint">
            <button type="button" class="link" onclick={() => openTab(algorithmSpec(d.name, d.version, algorithm.uid, algorithm.name), { pin: true })}>{algorithm.name}</button>
            — {algorithm.type}, {algorithm.language}. {algorithm.description}
          </p>
        {/if}
      </section>

      <section class="card">
        <h3>Parameter values</h3>
        {#if algorithm}
          <AlgorithmParamValues params={algorithm.params} bind:values={inst.values} readonly={d.readonly} idPrefix="inst" path="algorithmInstances[{index}].values" />
        {:else}
          <p class="empty">Choose an algorithm to set its parameters.</p>
        {/if}
      </section>
    </fieldset>

    <section class="card">
      <h3>Plugged in</h3>
      {#if plugs.length}
        <ul class="plain-list">{#each plugs as p}<li>{p}</li>{/each}</ul>
      {:else}
        <p class="empty">Not plugged yet: plug it on a node type (validators) or a lifecycle transition (guards, actions) in the domain editor.</p>
      {/if}
    </section>

    {#if algorithm}<AlgorithmTryIt {algorithm} values={inst.values} />{/if}
  {/if}
</div>

<style>
  .plain-list {
    list-style: none;
    margin: 0.2rem 0;
    padding: 0;
    display: grid;
    gap: 0.15rem;
  }
  .dirty {
    color: var(--warn);
    font-size: 0.85rem;
    font-weight: 600;
  }
</style>
