<script lang="ts">
  // The conditions of a field (shown when / required when): each tests the value of an earlier field; all must hold.
  import type { Condition, FieldDef } from '../types';
  import { opOf, withOp, type ConditionOp } from '../design';
  import { strings } from '../validate';
  import StringList from './StringList.svelte';

  let {
    id,
    label,
    fields,
    value = [],
    disabled = false,
    onchange,
  }: { id: string; label: string; fields: FieldDef[]; value?: Condition[]; disabled?: boolean; onchange: (v: Condition[] | undefined) => void } = $props();

  // a condition tests a plain value: no explanation, links, group, new node, decision or file
  const testable = $derived(fields.filter((f) => f.name && !['markdown', 'link', 'group', 'newNode', 'decision', 'file'].includes(f.widget)));
  const OPS: [ConditionOp, string][] = [
    ['equals', 'is'],
    ['in', 'is one of'],
    ['set', 'is filled in'],
    ['unset', 'is blank'],
  ];

  const emit = (cs: Condition[]) => onchange(cs.length ? cs : undefined);
  const put = (i: number, c: Condition) => emit(value.map((x, j) => (j === i ? c : x)));
  const fieldOf = (name: string) => fields.find((f) => f.name === name);
  const options = (name: string) => strings(fieldOf(name)?.constraints?.['options']);
  const cast = (name: string, v: string): unknown => (fieldOf(name)?.widget === 'number' && v !== '' && !isNaN(Number(v)) ? Number(v) : v);
  function add() {
    const f = testable[0];
    if (f) emit([...value, { field: f.name, set: true }]);
  }
</script>

<div class="conds">
  <span class="lbl">{label}</span>
  {#each value as c, i (i)}
    {@const op = opOf(c)}
    {@const f = fieldOf(c.field)}
    <div class="cond">
      <select aria-label="Field tested" value={c.field} {disabled} onchange={(e) => put(i, { ...c, field: e.currentTarget.value })}>
        {#if !f}<option value={c.field}>{c.field || '(none)'}</option>{/if}
        {#each testable as t (t.name)}<option value={t.name}>{t.label || t.name}</option>{/each}
      </select>
      <select aria-label="Test" value={op} {disabled} onchange={(e) => put(i, withOp(c, e.currentTarget.value as ConditionOp))}>
        {#each OPS as [k, l] (k)}<option value={k}>{l}</option>{/each}
      </select>
      {#if op === 'equals'}
        {#if f?.widget === 'boolean'}
          <select aria-label="Value" value={String(c.equals)} {disabled} onchange={(e) => put(i, { field: c.field, equals: e.currentTarget.value === 'true' })}>
            <option value="true">yes</option><option value="false">no</option>
          </select>
        {:else if options(c.field).length}
          <select aria-label="Value" value={String(c.equals ?? '')} {disabled} onchange={(e) => put(i, { field: c.field, equals: e.currentTarget.value })}>
            {#each options(c.field) as o (o)}<option value={o}>{o}</option>{/each}
          </select>
        {:else}
          <input aria-label="Value" type="text" value={String(c.equals ?? '')} {disabled} onchange={(e) => put(i, { field: c.field, equals: cast(c.field, e.currentTarget.value) })} />
        {/if}
      {:else if op === 'in'}
        <StringList id="{id}-{i}" value={(c.in ?? []).map(String)} suggestions={options(c.field)} {disabled}
          onchange={(l) => put(i, { field: c.field, in: l.map((x) => cast(c.field, x)) })} />
      {/if}
      {#if !disabled}<button type="button" class="small" aria-label="Remove the condition" onclick={() => emit(value.filter((_, j) => j !== i))}>×</button>{/if}
    </div>
  {/each}
  {#if !disabled}
    <button type="button" class="small" disabled={!testable.length} title={testable.length ? '' : 'no earlier field to test'} onclick={add}>+ condition</button>
  {/if}
</div>

<style>
  .conds {
    margin-bottom: 0.5rem;
  }
  .lbl {
    display: block;
    font-size: 0.85rem;
    margin-bottom: 0.2rem;
  }
  .cond {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
    align-items: center;
    margin-bottom: 0.25rem;
  }
  .cond select,
  .cond input {
    width: auto;
    max-width: 8rem;
  }
</style>
