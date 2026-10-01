<script lang="ts">
  // "Process" tab (ADR 0034): the steps and sub-steps that reach the objective of a change, each done by an action,
  // alternative actions, an agent, a nested process or a person. The process is run by an agent of its name towards a
  // goal of its name; a step that runs an agent or a process starts it as a sub-agent on the same change.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import StepEditor from './StepEditor.svelte';
  import ReferencesEditor from './ReferencesEditor.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { emptyStep, walkSteps, fromForm } from '../../methodologyForm';
  import ProcessGraphView from '../../components/ProcessGraphView.svelte';
  import { registry, errorMessage, type ProcessGraph, type PlanPreview } from '../../api';
  import { draftOf, draftActions, removeItemAction, syncTabUid, openItem } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => draftOf(tab));
  const index = $derived(d.indexOf('processes', tab.params.uid ?? '', tab.params.name ?? ''));
  const item = $derived(index >= 0 ? d.form.processes[index] : undefined);
  const p = $derived(`processes[${index}]`);
  let root = $state<HTMLElement>();
  let nameAtFocus = '';

  $effect(() => syncTabUid(tab, 'processes', item));
  provideActions(
    () => tab.id,
    () => draftActions(d, removeItemAction(d, 'processes', tab, () => index)),
  );
  useReveal(
    () => tab.id,
    () => root,
  );

  const all = $derived(item ? walkSteps(item.steps) : []);
  const byMethod = $derived(
    all.reduce<Record<string, number>>((acc, x) => {
      acc[x.step.method] = (acc[x.step.method] ?? 0) + 1;
      return acc;
    }, {}),
  );
  // the processes of this methodology this one nests, and the ones nesting it
  const nests = $derived(item ? [...new Set(all.filter((x) => x.step.method === 'process').map((x) => x.step.process))] : []);
  const nestedIn = $derived(
    item ? d.form.processes.filter((o) => o !== item && walkSteps(o.steps).some((x) => x.step.method === 'process' && x.step.process === item.name)) : [],
  );
  // the process as a graph, from the draft as edited (ADR 0036 §4), rebuilt a moment after an edit
  let view = $state<'graph' | 'steps'>('graph');
  let graph = $state<ProcessGraph | undefined>();
  let graphIssue = $state('');
  const snapshot = $derived(JSON.stringify(d.form));
  $effect(() => {
    void snapshot;
    const name = item?.name;
    if (!name || view !== 'graph') return;
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      registry
        .processGraph(fromForm(d.form).methodology, name, ctl.signal)
        .then((r) => {
          graph = r.graph;
          graphIssue = r.issues?.length ? `The draft does not compile (${r.issues.length} issue(s), see the Issues console): the graph shows its last valid state.` : '';
        })
        .catch((e) => {
          if (!ctl.signal.aborted) graphIssue = errorMessage(e);
        });
    }, 400);
    return () => (clearTimeout(timer), ctl.abort());
  });

  // the plan the process's agent (goap, utility or hybrid) actually reaches from the conditions toggled in the
  // graph view, overridden on top of an empty blackboard (ADR 0034; no live Change needed)
  let overrides = $state<Record<string, boolean>>({});
  let plan = $state<PlanPreview | undefined>();
  let planIssue = $state('');
  $effect(() => {
    void snapshot;
    const name = item?.name;
    const ov = overrides;
    if (!name || view !== 'graph') return;
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      registry
        .previewPlan(fromForm(d.form).methodology, name, name, ov, ctl.signal)
        .then((r) => {
          plan = r.preview;
          planIssue = r.issues?.length ? r.issues.map((i) => i.message).join('; ') : '';
        })
        .catch((e) => {
          if (!ctl.signal.aborted) planIssue = errorMessage(e);
        });
    }, 300);
    return () => (clearTimeout(timer), ctl.abort());
  });
  function setOverride(name: string, value: boolean | undefined) {
    if (value === undefined) {
      const { [name]: _, ...rest } = overrides;
      overrides = rest;
    } else {
      overrides = { ...overrides, [name]: value };
    }
  }
  // plans toward an agent/goal that is not the process itself (a method's actor): where a hybrid or utility
  // planner, if configured, actually runs (ADR 0034: the process's own agent is always goap)
  function previewAgent(agent: string, goal: string) {
    return registry.previewPlan(fromForm(d.form).methodology, agent, goal, overrides);
  }
  let overridesFor = '';
  $effect(() => {
    if (item?.name !== overridesFor) {
      overridesFor = item?.name ?? '';
      overrides = {};
    }
  });
  function openProcess(name: string) {
    const o = d.form.processes.find((x) => x.name === name);
    if (o) openItem(d, 'processes', o);
  }

  const agentsUsed = $derived([...new Set(all.filter((x) => x.step.method === 'agent' && x.step.agent).map((x) => x.step.agent))]);
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} icon="list" kind="Process" title={item.name || '(unnamed)'} dirty={d.itemDirty('processes', item.uid)} />
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <div class="grid">
          <div class="field">
            <label for="p-name">Name <span class="opt">(also the name of the agent that runs it and of its goal)</span></label>
            <input
              id="p-name"
              type="text"
              class="mono"
              bind:value={item.name}
              class:bad={d.bad(`${p}.name`)}
              data-path="{p}.name"
              placeholder="software_delivery"
              onfocus={() => (nameAtFocus = item.name)}
              onchange={() => {
                if (nameAtFocus && nameAtFocus !== item.name) d.rename('processes', nameAtFocus, item.name);
                nameAtFocus = item.name;
              }}
            />
          </div>
        </div>
        <div class="grid2">
          <div class="field">
            <label for="p-desc">Description</label>
            <input id="p-desc" type="text" bind:value={item.description} data-path="{p}.description" />
          </div>
          <div class="field">
            <label for="p-ex">Intent examples <span class="opt">(one per line)</span></label>
            <textarea id="p-ex" rows="2" bind:value={item.examples} data-path="{p}.examples"></textarea>
          </div>
        </div>
        <ReferencesEditor bind:refs={item.references} path="{p}.references" bad={d.bad} readonly={d.readonly} />
      </section>

    </fieldset>
      <div class="row switch" role="tablist" aria-label="View">
        <button type="button" role="tab" class="small" class:primary={view === 'graph'} aria-selected={view === 'graph'} onclick={() => (view = 'graph')}>Graph</button>
        <button type="button" role="tab" class="small" class:primary={view === 'steps'} aria-selected={view === 'steps'} onclick={() => (view = 'steps')}>Steps</button>
      </div>
      {#if view === 'graph'}
        <section class="card">
          {#if graphIssue}<p class="hint">{graphIssue}</p>{/if}
          {#if graph}
            <ProcessGraphView {graph} onprocess={openProcess} {plan} {planIssue} {overrides} {setOverride} {previewAgent} />
          {:else if !graphIssue}
            <p class="empty">Building the graph…</p>
          {/if}
        </section>
      {:else}
      <fieldset class="plain" disabled={d.readonly}>
      <section class="card" data-path="{p}.steps">
        <h3>Steps <span class="hint">{all.length}</span></h3>
        <p class="hint">
          A step is described with the precision the methodology has for it: by hand, with sub-steps, by an action or
          alternative actions the planner chooses among, by an agent that plans towards its goal, or by another process
          nested in it. Steps are not ordered by their position: the planner sequences them by their conditions — a step
          can start once its entry conditions hold (those of the steps containing it, its own, and those of what it runs)
          and it makes its exit criteria true. A manual step's condition <code>step:&lt;process&gt;/&lt;path&gt;</code> can
          be named by any other step.
        </p>
        {#each item.steps as step, i (step.key)}
          <StepEditor bind:step={item.steps[i]} siblings={item.steps} index={i} path="{p}.steps[{i}]" draft={d} />
        {/each}
        {#if !d.readonly}
          <button type="button" class="small primary" onclick={() => item.steps.push(emptyStep(`step_${item.steps.length + 1}`))}>+ Step</button>
        {/if}
      </section>
      </fieldset>
      {/if}
    <p class="hint">
      {Object.entries(byMethod)
        .map(([m, n]) => `${n} ${m}`)
        .join(' · ') || 'No step yet.'}
      {#if agentsUsed.length}
        · agents:
        {#each agentsUsed as a, i (a)}
          {@const ag = d.form.agents.find((x) => x.name === a)}
          {i ? ', ' : ''}{#if ag}<button type="button" class="link" onclick={() => openItem(d, 'agents', ag)}>{a}</button>{:else}{a}{/if}
        {/each}
      {/if}
      {#if nests.length}
        · nests: {nests.join(', ')}
      {/if}
      {#if nestedIn.length}
        · nested in:
        {#each nestedIn as o, i (o.uid)}{i ? ', ' : ''}<button type="button" class="link" onclick={() => openItem(d, 'processes', o)}>{o.name}</button>{/each}
      {/if}
    </p>
  {:else}
    <ItemMissing draft={d} what="Process" />
  {/if}
</div>

<style>
  .switch {
    display: flex;
    gap: 4px;
    margin: 4px 0 8px;
  }
</style>
