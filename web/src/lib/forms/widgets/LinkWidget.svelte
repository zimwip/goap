<script lang="ts">
  // Links to create from the source the field names (a node field, or a fixed key) to nodes found by a search, of the
  // link types its constraints allow.
  import type { WidgetProps } from '../registry';
  import type { LinkValue, NodeOption } from '../types';
  import { completeLinks, linkSources, linkTargets, linkTypes, newNodeTypes, nodeAllowed } from '../validate';
  import NodePicker from './NodePicker.svelte';
  import { typeName } from '../../stores/types.svelte';

  let { id, field, value, onchange, values, source, disabled }: WidgetProps = $props();
  const links = $derived(completeLinks(field, value, values));
  const sources = $derived(linkSources(field, values));
  const byKey = $derived(new Map(source.nodes.map((n) => [n.key, n])));
  let from = $state('');
  const current = $derived(sources.includes(from) ? from : (sources[0] ?? ''));
  // a source the form creates (a new node field) carries its type in its value
  const created = $derived(newNodeTypes({ fields: [{ name: String(field.constraints?.['from'] ?? ''), widget: 'newNode' }] }, values));
  const types = $derived(linkTypes(field, byKey.get(current)?.type ?? created.get(current), source));
  let type = $state('');
  const currentType = $derived(types.includes(type) ? type : (types[0] ?? ''));
  const targets = $derived(linkTargets(field, source));
  const label = (t: string) => source.linkLabel?.(t) || typeName(t);
  const max = $derived(typeof field.constraints?.['max'] === 'number' ? (field.constraints['max'] as number) : Infinity);

  function add(n: NodeOption) {
    if (!current || !currentType) return;
    source.remember?.(n);
    onchange([...links, { from: current, type: currentType, to: n.key } satisfies LinkValue]);
  }
  function drop(i: number) {
    onchange(links.filter((_, j) => j !== i));
  }
</script>

{#if links.length}
  <ul class="links">
    {#each links as l, i (i)}
      <li>
        <code>{l.from}</code>
        <span class="type">{label(l.type ?? '')} →</span>
        <code>{l.to}</code>
        <span class="title">{byKey.get(l.to ?? '')?.title ?? ''}</span>
        {#if !disabled}<button type="button" class="small" aria-label="Remove the link to {l.to}" onclick={() => drop(i)}>×</button>{/if}
      </li>
    {/each}
  </ul>
{/if}
{#if !sources.length}
  <p class="hint">Choose the source node first.</p>
{:else if !types.length}
  <p class="hint">No link type is allowed from {current}.</p>
{:else if links.length < max}
  <div class="add">
    {#if sources.length > 1}
      <select aria-label="Source" bind:value={from} {disabled}>
        {#each sources as s (s)}<option value={s}>{s}</option>{/each}
      </select>
    {/if}
    {#if types.length > 1}
      <select aria-label="Link type" bind:value={type} {disabled}>
        {#each types as t (t)}<option value={t}>{label(t)}</option>{/each}
      </select>
    {:else}
      <span class="type">{label(currentType)} →</span>
    {/if}
    <div class="grow">
      <NodePicker {id} choices={targets} exclude={links.filter((l) => l.from === current && l.type === currentType).map((l) => l.to ?? '')}
        placeholder={field.placeholder ?? 'Search a target…'} {disabled} search={source.search} allow={(n) => nodeAllowed(n, field.constraints, source, 'to')} onpick={add} />
    </div>
  </div>
{/if}

<style>
  .links {
    list-style: none;
    margin: 0 0 0.35rem;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
  .links li {
    display: grid;
    grid-template-columns: auto auto auto 1fr auto;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
  }
  .add {
    display: flex;
    gap: 0.4rem;
    align-items: center;
  }
  .grow {
    flex: 1;
  }
  .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .type,
  .hint {
    color: var(--muted);
    font-size: 0.85rem;
  }
  .hint {
    margin: 0;
  }
</style>
