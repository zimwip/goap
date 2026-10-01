<script lang="ts">
  // Inputs (entry conditions) and outputs (exit criteria) of a step. Each is a condition of the methodology and the
  // value it must have: a condition can be created on the spot, either static (a document of a type is present) or
  // dynamic (a CEL expression over the blackboard, evaluated as the run goes).
  import CondRows from '../../components/CondRows.svelte';
  import { emptyCondition, stepConditionNames, type CondRow, type StepForm } from '../../methodologyForm';
  import type { Draft } from '../../stores/drafts.svelte';

  let { step, path, draft: d }: { step: StepForm; path: string; draft: Draft } = $props();

  const options = $derived([...d.conditionOptions, ...stepConditionNames(d.form)]);

  let target = $state<'pre' | 'done' | ''>('');
  let name = $state('');
  let mode = $state<'static' | 'dynamic'>('static');
  let value = $state('');

  const nameOk = $derived(/^[a-z][a-z0-9_-]*$/.test(name) && !options.includes(name));

  function create() {
    if (!target || !nameOk || !value.trim()) return;
    const c = emptyCondition();
    c.name = name;
    c.expr = mode === 'static' ? `artifacts.exists(a, a.type == ${JSON.stringify(value.trim())})` : value.trim();
    c.description = mode === 'static' ? `${value.trim()} exists` : '';
    d.form.conditions.push(c);
    (step[target] as CondRow[]).push({ cond: name, value: true });
    name = value = '';
    target = '';
  }
</script>

<div class="io">
  <div class="grid2">
    <CondRows bind:rows={step.pre} {options} path="{path}.pre" label="Inputs — entry conditions (they sequence the steps)" bad={d.bad} readonly={d.readonly} />
    <CondRows bind:rows={step.done} {options} path="{path}.done" label="Outputs — done when (default: from how it is done)" bad={d.bad} readonly={d.readonly} />
  </div>
  {#if !d.readonly}
    <div class="row new">
      <button type="button" class="small" class:primary={target === 'pre'} onclick={() => (target = target === 'pre' ? '' : 'pre')}>+ New input</button>
      <button type="button" class="small" class:primary={target === 'done'} onclick={() => (target = target === 'done' ? '' : 'done')}>+ New output</button>
    </div>
    {#if target}
      <div class="card create">
        <div class="grid2">
          <div class="field">
            <label for="{step.key}-cn">Condition name</label>
            <input id="{step.key}-cn" type="text" class="mono" bind:value={name} placeholder="design_approved" />
            {#if name && !nameOk}<p class="hint">Lowercase letters, digits, - or _, starting with a letter, and not already declared.</p>{/if}
          </div>
          <div class="field">
            <label for="{step.key}-cm">Kind</label>
            <select id="{step.key}-cm" bind:value={mode}>
              <option value="static">Static — a document of a type exists</option>
              <option value="dynamic">Dynamic — a CEL expression</option>
            </select>
          </div>
        </div>
        <div class="field">
          <label for="{step.key}-cv">{mode === 'static' ? 'Document type' : 'CEL expression'}</label>
          <input id="{step.key}-cv" type="text" class="mono" bind:value placeholder={mode === 'static' ? 'ReleaseNote' : 'size(impacts) > 0'} />
        </div>
        <button type="button" class="small primary" disabled={!nameOk || !value.trim()} onclick={create}
          >Create the condition as {target === 'pre' ? 'an input' : 'an output'}</button
        >
      </div>
    {/if}
  {/if}
</div>

<style>
  .new {
    display: flex;
    gap: 6px;
    margin-top: 6px;
  }
  .create {
    margin-top: 6px;
  }
</style>
