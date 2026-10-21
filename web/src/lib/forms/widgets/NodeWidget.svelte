<script lang="ts">
  // A node of the graph (or several), found by a search among those its constraints allow.
  import type { WidgetProps } from '../registry';
  import type { NodeOption } from '../types';
  import { nodeAllowed, nodeChoices, strings } from '../validate';
  import NodePicker from './NodePicker.svelte';
  import { typeName } from '../../stores/types.svelte';

  let { id, field, value, onchange, source, disabled }: WidgetProps = $props();
  const multiple = $derived(field.constraints?.['multiple'] === true);
  const chosen = $derived(multiple ? strings(value) : typeof value === 'string' && value ? [value] : []);
  const choices = $derived(nodeChoices(field, source));
  const byKey = $derived(new Map(source.nodes.map((n) => [n.key, n])));

  function pick(n: NodeOption) {
    source.remember?.(n);
    onchange(multiple ? [...chosen, n.key] : n.key);
  }
  function drop(key: string) {
    onchange(multiple ? chosen.filter((k) => k !== key) : undefined);
  }
</script>

{#if chosen.length}
  <ul class="chosen">
    {#each chosen as k (k)}
      {@const n = byKey.get(k)}
      <li>
        <code>{k}</code>
        {#if n}<span class="title">{n.title ?? ''}</span><span class="type">{typeName(n.type)}</span>{/if}
        {#if !disabled}<button type="button" class="small" aria-label="Remove {k}" onclick={() => drop(k)}>×</button>{/if}
      </li>
    {/each}
  </ul>
{/if}
{#if multiple || !chosen.length}
  <NodePicker {id} {choices} exclude={chosen} placeholder={field.placeholder} {disabled} search={source.search} allow={(n) => nodeAllowed(n, field.constraints, source)} onpick={pick} />
{/if}

<style>
  .chosen {
    list-style: none;
    margin: 0 0 0.35rem;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
  .chosen li {
    display: grid;
    grid-template-columns: auto 1fr auto auto;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
  }
  .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .type {
    color: var(--muted);
    font-size: 0.8rem;
  }
</style>
