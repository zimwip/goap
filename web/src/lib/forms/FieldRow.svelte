<script lang="ts">
  // One field of a form: its label, its widget (the JSON fallback when none is registered), its help and its issues.
  import type { FieldDef, FormValues, NodeSource } from './types';
  import { widgetOf } from './registry';
  import JsonWidget from './widgets/JsonWidget.svelte';

  let {
    id,
    field,
    value,
    onchange,
    values,
    source,
    disabled = false,
    required = false,
    issues = [],
  }: {
    id: string;
    field: FieldDef;
    value: unknown;
    onchange: (v: unknown) => void;
    values: FormValues;
    source: NodeSource;
    disabled?: boolean;
    required?: boolean;
    /** the issues to show */
    issues?: string[];
  } = $props();
  const w = $derived(widgetOf(field.widget));
</script>

{#if w?.display}
  <div class="display"><w.component {id} {field} value={undefined} onchange={() => {}} {values} {source} /></div>
{:else}
  <div class="field" class:invalid={issues.length > 0}>
    <label for={id}>{field.label || field.name}{#if required}<span class="req" aria-label="required"> *</span>{/if}</label>
    {#if w}
      <w.component {id} {field} {value} {onchange} {values} {source} {disabled} invalid={issues.length > 0} />
    {:else}
      <JsonWidget {id} {field} {value} {onchange} {values} {source} {disabled} invalid={issues.length > 0} />
    {/if}
    {#if field.help}<p class="help">{field.help}</p>{/if}
    {#each issues as msg (msg)}<p class="issue" role="alert">{msg}</p>{/each}
  </div>
{/if}

<style>
  .field {
    margin-bottom: 0.7rem;
  }
  .field :global(input:not([type='checkbox']):not([type='radio']):not([type='file'])),
  .field :global(textarea),
  .field :global(select) {
    width: 100%;
  }
  .req {
    color: var(--danger);
  }
  .help {
    color: var(--muted);
    font-size: 0.8rem;
    margin: 0.2rem 0 0;
  }
  .issue {
    color: var(--danger);
    font-size: 0.8rem;
    margin: 0.2rem 0 0;
  }
  .invalid > :global(input),
  .invalid > :global(textarea),
  .invalid > :global(select) {
    border-color: var(--danger);
  }
  .display {
    margin-bottom: 0.6rem;
  }
</style>
