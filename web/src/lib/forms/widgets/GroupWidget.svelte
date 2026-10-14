<script lang="ts">
  // Entries the person repeats, each made of the sub-fields of the group (their conditions read the entry's values).
  import type { WidgetProps } from '../registry';
  import type { FormValues } from '../types';
  import { fieldsIssues, initialValues, isRequired, visibleState } from '../validate';
  import FieldRow from '../FieldRow.svelte';

  let { id, field, value, onchange, source, disabled }: WidgetProps = $props();
  const entries = $derived(Array.isArray(value) ? (value as FormValues[]) : []);
  const max = $derived(typeof field.constraints?.['max'] === 'number' ? (field.constraints['max'] as number) : Infinity);

  function add() {
    onchange([...entries, initialValues({ fields: field.fields })]);
  }
  function drop(i: number) {
    onchange(entries.filter((_, j) => j !== i));
  }
  function set(i: number, name: string, v: unknown) {
    onchange(entries.map((e, j) => (j === i ? { ...e, [name]: v } : e)));
  }
</script>

<div class="group" {id}>
  {#each entries as entry, i (i)}
    {@const st = visibleState(field.fields, entry)}
    {@const issues = fieldsIssues(field.fields, entry, source)}
    <div class="entry">
      <div class="head">
        <span class="n">{field.label || field.name} {i + 1}</span>
        <button type="button" class="link" {disabled} onclick={() => drop(i)} aria-label={`Remove entry ${i + 1}`}>Remove</button>
      </div>
      {#each (field.fields ?? []).filter((f) => st.visible.has(f.name)) as f (f.name)}
        <FieldRow
          id={`${id}-${i}-${f.name}`}
          field={f}
          value={entry[f.name]}
          onchange={(v: unknown) => set(i, f.name, v)}
          values={st.seen}
          {source}
          {disabled}
          required={isRequired(f, st.seen)}
          issues={entry[f.name] !== undefined ? (issues[f.name] ?? []) : []}
        />
      {/each}
    </div>
  {/each}
  <button type="button" disabled={disabled || entries.length >= max} onclick={add}>+ Add {field.label ? field.label.toLowerCase() : 'an entry'}</button>
</div>

<style>
  .group {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    align-items: flex-start;
  }
  .entry {
    align-self: stretch;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.4rem 0.6rem 0;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.3rem;
  }
  .n {
    font-size: 0.8rem;
    color: var(--muted);
    font-weight: 600;
  }
  button.link {
    background: none;
    border: none;
    color: var(--muted);
    cursor: pointer;
    padding: 0;
    font-size: 0.8rem;
  }
</style>
