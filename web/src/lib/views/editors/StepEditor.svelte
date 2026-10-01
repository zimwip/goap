<script lang="ts">
  // One step of a process and, recursively, its sub-steps (ADR 0034): its name, how it is done (by hand, sub-steps,
  // an action, alternative actions, variants (methods), a nested process), its entry conditions and exit criteria.
  import { untrack } from 'svelte';
  import StepEditor from './StepEditor.svelte';
  import StepIO from './StepIO.svelte';
  import RowTools from '../../components/RowTools.svelte';
  import PickList from './PickList.svelte';
  import ReferencesEditor from './ReferencesEditor.svelte';
  import ResponsibilitiesEditor from './ResponsibilitiesEditor.svelte';
  import { STEP_METHODS, emptyStep, moveItem, type StepForm } from '../../methodologyForm';
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
  const processNames = $derived(f.processes.map((p) => p.name).filter(Boolean));
  const capabilities = $derived([...new Set(f.methods.map((m) => m.for.trim()).filter(Boolean))]);
  const providers = $derived(f.methods.filter((m) => m.for.trim() && m.for === step.capability));
  const listId = $derived(`procs-${step.key}`);

  function summary(s: StepForm): string {
    switch (s.method) {
      case 'steps':
        return `${s.steps.length} sub-step(s)`;
      case 'action':
        return s.action ? `action ${s.action}` : 'action ?';
      case 'actions':
        return s.actions.length ? `one of ${s.actions.join(', ')}` : 'actions ?';
      case 'process':
        return s.process ? `process ${s.process}` : 'process ?';
      case 'capability':
        return s.capability ? `method ${s.capability}` : 'method ?';
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
          <label for="{step.key}-method">Made of</label>
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
      {:else if step.method === 'capability'}
        <div class="field">
          <label for="{step.key}-cap">Capability <span class="opt">(the methods providing it say how, per context, and which agent acts)</span></label>
          <select id="{step.key}-cap" bind:value={step.capability} class:bad={d.bad(`${path}.method`)} data-path="{path}.method">
            <option value="">— capability —</option>
            {#if step.capability && !capabilities.includes(step.capability)}<option value={step.capability}>{step.capability} (no method)</option>{/if}
            {#each capabilities as c (c)}<option value={c}>{c}</option>{/each}
          </select>
          {#if providers.length}
            <p class="hint">
              Methods: {#each providers as m, i (m.uid)}{i ? ', ' : ''}<code>{m.name}</code> ({m.when ? `when ${m.when}` : 'always'}{m.priority ? `, priority ${m.priority}` : ''}){/each}
            </p>
          {/if}
        </div>
        <div class="grid">
          <div class="field">
            <label for="{step.key}-foreach">For each <span class="opt">(CEL list; one parallel stream per element, <code>vars.item</code>; empty: once)</span></label>
            <input id="{step.key}-foreach" type="text" class="mono" bind:value={step.foreach} class:bad={d.bad(`${path}.foreach`)} data-path="{path}.foreach" placeholder="vars.components" />
          </div>
          {#if step.foreach.trim()}
            <div class="field">
              <label for="{step.key}-groupby">Group by <span class="opt">(CEL key per element; one stream per group, item is {'{key, items}'})</span></label>
              <input id="{step.key}-groupby" type="text" class="mono" bind:value={step.groupBy} class:bad={d.bad(`${path}.groupBy`)} data-path="{path}.groupBy" placeholder="vars.item.lang" />
            </div>
          {/if}
        </div>
      {/if}

      <details class="more" open={!!step.guidance || !!step.checklist.trim() || !!step.roles.responsible || !!step.roles.accountable}>
        <summary>Roles, guidance, checklist, deliverables</summary>
        <ResponsibilitiesEditor
          bind:roles={step.roles}
          declared={f.roles.map((r) => r.name).filter(Boolean)}
          path="{path}.roles"
          bad={d.bad}
          hint="(empty: those of the step containing it; a method's roles replace them)"
        />
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
        <summary>Inputs, outputs, reference documents</summary>
        <StepIO {step} {path} draft={d} />
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
