<script lang="ts">
  // "Method" tab (ADR 0035 §1): the documentary reference of how a step capability is carried out in a context — its
  // guidance, checklist, deliverables and reference documents — and the actions that realize it (ADR 0050). A step
  // names the capability; the applicable method (its context holds; highest priority, then the most specific
  // context) is chosen when the step runs, and an agent instance is created to apply it.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import CondRows from '../../components/CondRows.svelte';
  import PickList from './PickList.svelte';
  import ModelAliasField from '../../components/ModelAliasField.svelte';
  import { modelChoices, refreshModelChoices } from '../../stores/modelChoices.svelte';
  import ReferencesEditor from './ReferencesEditor.svelte';
  import ResponsibilitiesEditor from './ResponsibilitiesEditor.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { emptyStep, walkSteps, PLANNERS, LLM_PLANNERS } from '../../methodologyForm';
  import StepEditor from './StepEditor.svelte';
  import DraftFlow from '../../components/DraftFlow.svelte';
  import { draftOf, draftActions, removeItemAction, syncTabUid, openItem, openStep } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => draftOf(tab));
  const index = $derived(d.indexOf('methods', tab.params.uid ?? '', tab.params.name ?? ''));
  const item = $derived(index >= 0 ? d.form.methods[index] : undefined);
  const p = $derived(`methods[${index}]`);
  let root = $state<HTMLElement>();

  $effect(() => syncTabUid(tab, 'methods', item));
  provideActions(
    () => tab.id,
    () => draftActions(d, removeItemAction(d, 'methods', tab, () => index)),
  );
  useReveal(
    () => tab.id,
    () => root,
  );

  const actionNames = $derived([...new Set(d.form.actions.map((a) => a.name.trim()).filter(Boolean))]);
  const actionDetails = $derived(Object.fromEntries(d.form.actions.map((a) => [a.name, a.kind])));
  const needsModel = $derived(!!item && (LLM_PLANNERS as readonly string[]).includes(item.planner));
  $effect(() => {
    if (needsModel && !modelChoices.loaded) void refreshModelChoices();
  });
  const capabilities = $derived([...new Set(d.form.methods.map((m) => m.for.trim()).filter(Boolean))]);
  // the other methods of the same capability, and the steps that name it
  const siblings = $derived(item ? d.form.methods.filter((m) => m !== item && m.for.trim() && m.for === item.for) : []);
  const usedBy = $derived(
    item
      ? d.form.processes.flatMap((pr) =>
          walkSteps(pr.steps)
            .filter((x) => x.step.method === 'capability' && x.step.capability === item.for)
            .map((x) => ({ process: pr, path: `${pr.name}/${x.path}` })),
        )
      : [],
  );

  function openStepAt(path: string) {
    if (!item) return;
    let list = item.steps;
    let found;
    for (const n of path.split('/').slice(1)) {
      found = list.find((x) => x.name === n);
      if (!found) return;
      list = found.steps;
    }
    if (found) openStep(d, found, item, true);
  }
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} path={`methods[${index}]`} icon="book" kind="Method" title={item.name || '(unnamed)'} dirty={d.itemDirty('methods', item.uid)} />
    {#if (item.steps.length || item.actions.length) && item.name}
      <section class="card">
        <h3>{item.steps.length ? "Flow of the method's steps" : 'Actions of the method'}</h3>
        <DraftFlow draft={d} root={item.name} onstep={openStepAt} />
      </section>
    {/if}
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <div class="grid">
          <div class="field">
            <label for="me-name">Name</label>
            <input id="me-name" type="text" class="mono" bind:value={item.name} class:bad={d.bad(`${p}.name`)} data-path="{p}.name" placeholder="solution_design" />
          </div>
          <div class="field">
            <label for="me-for">Capability <span class="opt">(what a step names)</span></label>
            <input id="me-for" type="text" class="mono" list="me-caps" bind:value={item.for} class:bad={d.bad(`${p}.for`)} data-path="{p}.for" placeholder="design" />
            <datalist id="me-caps">{#each capabilities as c (c)}<option value={c}></option>{/each}</datalist>
          </div>
        </div>
        <div class="field">
          <label for="me-desc">Description</label>
          <input id="me-desc" type="text" bind:value={item.description} data-path="{p}.description" />
        </div>
        <div class="grid">
          <div class="field">
            <label for="me-when">Context <span class="opt">(CEL on the blackboard; empty: always)</span></label>
            <input id="me-when" type="text" class="mono" bind:value={item.when} class:bad={d.bad(`${p}.when`)} data-path="{p}.when" placeholder={'changeImpacts.exists(n, "alm@Component" in n.types)'} />
          </div>
          <div class="field">
            <label for="me-prio">Priority <span class="opt">(the highest applicable wins, then the most specific context)</span></label>
            <input id="me-prio" type="number" step="1" bind:value={item.priority} data-path="{p}.priority" />
          </div>
        </div>
      </section>

      <section class="card">
        <h3>Actions</h3>
        <p class="hint">
          The actions that realize the method. When a step names its capability, an agent instance is created for the
          step, acting as the responsible role: it plans over these actions and those of the steps below (a step
          inherits the pool) towards the goal of the method. Without steps, state what the method reaches.
        </p>
        <div class="grid2">
          <PickList
            bind:selected={item.actions}
            options={actionNames}
            details={actionDetails}
            allLabel="no pool (steps only)"
            label="Actions"
            path="{p}.actions"
            bad={d.bad}
            readonly={d.readonly}
          />
          {#if !item.steps.length}
            <div class="field">
              <CondRows bind:rows={item.done} options={d.conditionOptions} path="{p}.done" label="Conditions it reaches" bad={d.bad} readonly={d.readonly} />
            </div>
          {/if}
        </div>
        <div class="grid">
          <div class="field">
            <label for="me-planner">Planner</label>
            <select id="me-planner" bind:value={item.planner} class:bad={d.bad(`${p}.planner`)} data-path="{p}.planner">
              {#each PLANNERS as pl (pl)}<option value={pl}>{pl}</option>{/each}
            </select>
          </div>
          {#if needsModel}
            <div class="field">
              <label for="me-model">Model</label>
              <ModelAliasField id="me-model" bind:value={item.model} bad={d.bad(`${p}.model`)} disabled={d.readonly} />
            </div>
          {/if}
          <div class="field">
            <label for="me-mcps">MCPs <span class="opt">(comma separated; for its llm / script actions)</span></label>
            <input id="me-mcps" type="text" class="mono" bind:value={item.mcps} data-path="{p}.mcps" />
          </div>
        </div>
      </section>

      <section class="card" data-path="{p}.steps">
        <h3>Steps <span class="hint">{walkSteps(item.steps).length}</span></h3>
        <p class="hint">
          A method is an activity made of activities: its steps and sub-steps, down to actions, sequenced by their
          conditions like a process's. The agent instance created for the method performs them.
        </p>
        {#each item.steps as step, i (step.key)}
          <StepEditor bind:step={item.steps[i]} siblings={item.steps} index={i} path="{p}.steps[{i}]" draft={d} />
        {/each}
        {#if !d.readonly}
          <button type="button" class="small primary" onclick={() => item.steps.push(emptyStep(`step_${item.steps.length + 1}`))}>+ Step</button>
        {/if}
      </section>

      <section class="card">
        <h3>Reference</h3>
        <div class="field">
          <label for="me-guid">Guidance <span class="opt">(markdown; shown to the person, given to the agent)</span></label>
          <textarea id="me-guid" rows="4" bind:value={item.guidance} data-path="{p}.guidance"></textarea>
        </div>
        <div class="grid2">
          <div class="field">
            <label for="me-chk">Checklist <span class="opt">(one item per line)</span></label>
            <textarea id="me-chk" rows="3" bind:value={item.checklist} data-path="{p}.checklist"></textarea>
          </div>
          <div class="field">
            <label for="me-dlv">Deliverables <span class="opt">(comma separated)</span></label>
            <input id="me-dlv" type="text" bind:value={item.deliverables} data-path="{p}.deliverables" />
          </div>
        </div>
        <ReferencesEditor bind:refs={item.references} path="{p}.references" bad={d.bad} readonly={d.readonly} />
        <ResponsibilitiesEditor
          bind:roles={item.roles}
          declared={d.form.roles.map((r) => r.name).filter(Boolean)}
          path="{p}.roles"
          bad={d.bad}
          hint="(when set, they replace the roles of the step)"
        />
      </section>
    </fieldset>
    <p class="hint">
      Other methods for <code>{item.for || '?'}</code>:
      {#each siblings as m, i (m.uid)}{i ? ', ' : ''}<button type="button" class="link" onclick={() => openItem(d, 'methods', m)}>{m.name || '(unnamed)'}</button>{:else}none{/each}
      · steps naming it:
      {#each usedBy as u, i (u.path)}{i ? ', ' : ''}<button type="button" class="link" onclick={() => openItem(d, 'processes', u.process)}>{u.path}</button>{:else}none{/each}
    </p>
  {:else}
    <ItemMissing draft={d} what="Method" />
  {/if}
</div>
