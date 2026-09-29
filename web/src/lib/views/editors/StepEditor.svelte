<script lang="ts">
  // One step of a process and, recursively, its sub-steps (ADR 0034): its name, how it is done (by hand, sub-steps,
  // an action, alternative actions, an agent, a nested process), its entry conditions and exit criteria.
  import { untrack } from 'svelte';
  import StepEditor from './StepEditor.svelte';
  import CondRows from '../../components/CondRows.svelte';
  import RowTools from '../../components/RowTools.svelte';
  import PickList from './PickList.svelte';
  import ReferencesEditor from './ReferencesEditor.svelte';
  import { STEP_METHODS, emptyStep, moveItem, stepConditionNames, type StepForm } from '../../methodologyForm';
  import type { Draft } from '../../stores/drafts.svelte';

  let {
    step = $bindable(),
    siblings,
    index,
    path,
    draft: d,
    depth = 0,
  }: {
    step: StepForm;
    /** the steps of the same level (to reorder, remove, and pick the ones it waits for) */
    siblings: StepForm[];
    index: number;
    /** issue path of the step, e.g. "processes[0].steps[1]" */
    path: string;
    draft: Draft;
    depth?: number;
  } = $props();

  // the top steps unfolded, their sub-steps folded: the process reads as an outline
  let open = $state(untrack(() => depth === 0));
  const f = $derived(d.form);
  const actionNames = $derived(f.actions.filter((a) => a.name && !a.specializes.trim()).map((a) => a.name));
  const agentNames = $derived(f.agents.map((a) => a.name).filter(Boolean));
  const agent = $derived(f.agents.find((a) => a.name === step.agent));
  const goalNames = $derived((agent && agent.goals.length ? agent.goals : f.goals.map((g) => g.name)).filter(Boolean));
  const processNames = $derived(f.processes.map((p) => p.name).filter(Boolean));
  // the declared conditions and the conditions of the steps done once they have run
  const conditions = $derived([...d.conditionOptions, ...stepConditionNames(f)]);
  const listId = $derived(`procs-${step.key}`);

  function summary(s: StepForm): string {
    switch (s.method) {
      case 'steps':
        return `${s.steps.length} sub-step(s)`;
      case 'action':
        return s.action ? `action ${s.action}` : 'action ?';
      case 'actions':
        return s.actions.length ? `one of ${s.actions.join(', ')}` : 'actions ?';
      case 'agent':
        return s.agent ? `agent ${s.agent}${s.goal ? ` → ${s.goal}` : ''}` : 'agent ?';
      case 'process':
        return s.process ? `process ${s.process}` : 'process ?';
    }
    return 'by hand';
  }

  function addSub() {
    step.steps.push(emptyStep(`step_${step.steps.length + 1}`));
  }
</script>

<div class="step" class:bad={d.bad(path, true)} class:nested={depth > 0} data-path={path}>
  <div class="row head">
    <button type="button" class="ghost small icon" aria-label={open ? 'Fold the step' : 'Unfold the step'} aria-expanded={open} onclick={() => (open = !open)}
      >{open ? '▾' : '▸'}</button
    >
    <span class="num">{index + 1}.</span>
    <input
      type="text"
      class="mono name"
      aria-label="Step name"
      bind:value={step.name}
      class:bad={d.bad(`${path}.name`)}
      data-path="{path}.name"
      placeholder="step_name"
    />
    <span class="hint ell grow">{summary(step)}</span>
    {#if !d.readonly}
      <RowTools {index} count={siblings.length} label="the step" onmove={(delta) => moveItem(siblings, index, delta)} onremove={() => siblings.splice(index, 1)} />
    {/if}
  </div>
  {#if open}
    <div class="body">
      <div class="grid2">
        <div class="field">
          <label for="{step.key}-desc">Description</label>
          <input id="{step.key}-desc" type="text" bind:value={step.description} data-path="{path}.description" placeholder="What the step achieves" />
        </div>
        <div class="field">
          <label for="{step.key}-method">Done by</label>
          <select id="{step.key}-method" bind:value={step.method} data-path="{path}.method">
            {#each STEP_METHODS as m (m.id)}<option value={m.id}>{m.label}</option>{/each}
          </select>
        </div>
      </div>

      {#if step.method === 'manual'}
        <div class="field">
          <label for="{step.key}-instr">Instructions <span class="opt">(what a person does; the step is done once submitted)</span></label>
          <textarea id="{step.key}-instr" rows="3" bind:value={step.instructions} data-path="{path}.instructions"></textarea>
        </div>
      {:else if step.method === 'action'}
        <div class="field">
          <label for="{step.key}-action">Action</label>
          <select id="{step.key}-action" bind:value={step.action} class:bad={d.bad(`${path}.action`)} data-path="{path}.action">
            <option value="">— action —</option>
            {#if step.action && !actionNames.includes(step.action)}<option value={step.action}>{step.action} (unknown)</option>{/if}
            {#each actionNames as a (a)}<option value={a}>{a}</option>{/each}
          </select>
        </div>
      {:else if step.method === 'actions'}
        <PickList
          bind:selected={step.actions}
          options={actionNames}
          allLabel="none chosen"
          label="Alternative actions (the cheapest first, another one if it fails)"
          path="{path}.actions"
          bad={d.bad}
          readonly={d.readonly}
        />
      {:else if step.method === 'agent'}
        <div class="grid2">
          <div class="field">
            <label for="{step.key}-agent">Agent</label>
            <select id="{step.key}-agent" bind:value={step.agent} class:bad={d.bad(`${path}.agent`)} data-path="{path}.agent">
              <option value="">— agent —</option>
              {#if step.agent && !agentNames.includes(step.agent)}<option value={step.agent}>{step.agent} (unknown)</option>{/if}
              {#each agentNames as a (a)}<option value={a}>{a}</option>{/each}
            </select>
          </div>
          <div class="field">
            <label for="{step.key}-goal">Goal <span class="opt">(default: the agent's only goal)</span></label>
            <select id="{step.key}-goal" bind:value={step.goal} class:bad={d.bad(`${path}.goal`)} data-path="{path}.goal">
              <option value="">— default —</option>
              {#if step.goal && !goalNames.includes(step.goal)}<option value={step.goal}>{step.goal} (unknown)</option>{/if}
              {#each goalNames as g (g)}<option value={g}>{g}</option>{/each}
            </select>
          </div>
        </div>
      {:else if step.method === 'process'}
        <div class="field">
          <label for="{step.key}-proc">Process <span class="opt">(of this methodology, or &lt;methodology&gt;/&lt;process&gt;)</span></label>
          <input
            id="{step.key}-proc"
            type="text"
            class="mono"
            list={listId}
            bind:value={step.process}
            class:bad={d.bad(`${path}.process`)}
            data-path="{path}.process"
            placeholder="release_train"
          />
          <datalist id={listId}>{#each processNames as p (p)}<option value={p}></option>{/each}</datalist>
        </div>
      {/if}

      <details class="more" open={!!step.guidance || !!step.checklist.trim()}>
        <summary>Guidance, checklist, deliverables</summary>
        <div class="field">
          <label for="{step.key}-guid">Guidance <span class="opt">(markdown: what the step is for and how to go about it; shown to the person, given to the agent)</span></label>
          <textarea id="{step.key}-guid" rows="3" bind:value={step.guidance} data-path="{path}.guidance"></textarea>
        </div>
        <div class="grid2">
          <div class="field">
            <label for="{step.key}-chk">Checklist <span class="opt">(one item per line)</span></label>
            <textarea id="{step.key}-chk" rows="3" bind:value={step.checklist} data-path="{path}.checklist"></textarea>
          </div>
          <div class="field">
            <label for="{step.key}-dlv">Deliverables <span class="opt">(comma separated)</span></label>
            <input id="{step.key}-dlv" type="text" bind:value={step.deliverables} data-path="{path}.deliverables" placeholder="ReleaseNote, TestReport" />
          </div>
        </div>
      </details>
      <details class="more" open={step.pre.length > 0 || step.done.length > 0 || step.references.length > 0}>
        <summary>Entry conditions, exit criteria, reference documents</summary>
        <div class="grid2">
          <CondRows
            bind:rows={step.pre}
            options={conditions}
            path="{path}.pre"
            label="Entry conditions (with those of what it runs; they sequence the steps)"
            bad={d.bad}
            readonly={d.readonly}
          />
          <CondRows bind:rows={step.done} options={conditions} path="{path}.done" label="Done when (default: from how it is done)" bad={d.bad} readonly={d.readonly} />
        </div>
        <ReferencesEditor bind:refs={step.references} path="{path}.references" bad={d.bad} readonly={d.readonly} />
      </details>

      {#if step.method === 'steps'}
        <div class="subs">
          {#each step.steps as sub, i (sub.key)}
            <StepEditor bind:step={step.steps[i]} siblings={step.steps} index={i} path="{path}.steps[{i}]" draft={d} depth={depth + 1} />
          {/each}
          {#if !d.readonly}
            <button type="button" class="small" onclick={addSub}>+ Sub-step</button>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .step {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 6px 8px;
    margin: 6px 0;
    background: var(--bg);
  }
  .step.nested {
    background: var(--surface);
  }
  .step.bad {
    border-color: var(--danger);
  }
  .row.head {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .name {
    width: 14em;
    max-width: 40%;
  }
  .num {
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .ell {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .body {
    padding: 6px 0 0 22px;
  }
  .subs {
    margin-top: 6px;
    padding-left: 8px;
    border-left: 2px solid var(--border);
  }
  .more summary {
    cursor: pointer;
    color: var(--muted);
    margin: 4px 0;
  }
</style>
