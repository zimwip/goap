<script lang="ts">
  // "Method" tab (ADR 0035 §1): the documentary reference of how a step capability is carried out in a context — its
  // guidance, checklist, deliverables and reference documents — and the agent that acts. A step names the capability;
  // the applicable method (its context holds) with the highest priority is chosen when the step runs.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import ReferencesEditor from './ReferencesEditor.svelte';
  import ResponsibilitiesEditor from './ResponsibilitiesEditor.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { emptyStep, walkSteps } from '../../methodologyForm';
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

  const agentNames = $derived(d.form.agents.map((a) => a.name).filter(Boolean));
  const agent = $derived(item ? d.form.agents.find((a) => a.name === item.agent) : undefined);
  const goalNames = $derived((agent && agent.goals.length ? agent.goals : d.form.goals.map((g) => g.name)).filter(Boolean));
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
    <DraftHeader draft={d} icon="book" kind="Method" title={item.name || '(unnamed)'} dirty={d.itemDirty('methods', item.uid)} />
    {#if (item.steps.length || item.agent) && item.name}
      <section class="card">
        <h3>{item.agent ? `Actions operated by ${item.agent}` : "Flow of the method's steps"}</h3>
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
            <label for="me-prio">Priority <span class="opt">(the highest applicable wins)</span></label>
            <input id="me-prio" type="number" step="1" bind:value={item.priority} data-path="{p}.priority" />
          </div>
        </div>
      </section>

      <section class="card">
        <h3>Agent</h3>
        <p class="hint">The method says how and with what; the agent executes. With steps below, the agent performs the activities that compose the method and has access to them and to the tools their actions declare. Without steps, it reaches the goal with its own actions.</p>
        <div class="grid">
          <div class="field">
            <label for="me-agent">Agent</label>
            <select id="me-agent" bind:value={item.agent} class:bad={d.bad(`${p}.agent`)} data-path="{p}.agent">
              <option value="">— agent —</option>
              {#if item.agent && !agentNames.includes(item.agent)}<option value={item.agent}>{item.agent} (unknown)</option>{/if}
              {#each agentNames as a (a)}<option value={a}>{a}</option>{/each}
            </select>
            {#if agent}<button type="button" class="link" onclick={() => openItem(d, 'agents', agent)}>open the agent</button>{/if}
          </div>
          {#if !item.steps.length}
          <div class="field">
            <label for="me-goal">Goal <span class="opt">(default: the agent's only goal)</span></label>
            <select id="me-goal" bind:value={item.goal} class:bad={d.bad(`${p}.goal`)} data-path="{p}.goal">
              <option value="">— default —</option>
              {#if item.goal && !goalNames.includes(item.goal)}<option value={item.goal}>{item.goal} (unknown)</option>{/if}
              {#each goalNames as g (g)}<option value={g}>{g}</option>{/each}
            </select>
          </div>
          {/if}
        </div>
      </section>

      <section class="card" data-path="{p}.steps">
        <h3>Steps <span class="hint">{walkSteps(item.steps).length}</span></h3>
        <p class="hint">
          A method is an activity made of activities: its steps and sub-steps, down to actions, sequenced by their
          conditions like a process's. The agent above performs them; without one, an agent of the method's own name
          does.
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
