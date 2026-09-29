<script lang="ts">
  // "Process" tab (ADR 0034): the steps and sub-steps that reach the objective of a change, each done by an action,
  // alternative actions, an agent, a nested process or a person. The process is run by an agent of its name towards a
  // goal of its name; a step that runs an agent or a process starts it as a sub-agent on the same change.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import StepEditor from './StepEditor.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { emptyStep, walkSteps } from '../../methodologyForm';
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
          <div class="field">
            <label class="check"><input type="checkbox" bind:checked={item.parallel} /> Top steps in any order</label>
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
      </section>

      <section class="card" data-path="{p}.steps">
        <h3>Steps <span class="hint">{all.length}</span></h3>
        <p class="hint">
          A step follows the one before it (unless its level runs in any order) and is described with the precision the
          methodology has for it: by hand, with sub-steps, by an action or alternative actions the planner chooses among,
          by an agent that plans towards its goal, or by another process nested in it. Open "Entry conditions, exit
          criteria" to state more than the method implies.
        </p>
        {#each item.steps as step, i (step.key)}
          <StepEditor bind:step={item.steps[i]} siblings={item.steps} index={i} path="{p}.steps[{i}]" draft={d} parallel={item.parallel} />
        {/each}
        {#if !d.readonly}
          <button type="button" class="small primary" onclick={() => item.steps.push(emptyStep(`step_${item.steps.length + 1}`))}>+ Step</button>
        {/if}
      </section>
    </fieldset>
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
  .check {
    display: flex;
    gap: 6px;
    align-items: center;
    margin-top: 1.4em;
  }
</style>
