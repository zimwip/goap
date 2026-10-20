<script lang="ts">
  // The validators plugged on an attribute, a node type or a link type: instances of the algorithms of the domain,
  // checked when a draft is accepted and when the change commits, in this order. One component for the three levels.
  import { moveItem } from '../methodologyForm';
  import RowTools from './RowTools.svelte';

  let {
    list = $bindable(),
    instances,
    usage,
    noun = 'validator',
    bad = () => false,
    readonly = false,
    path,
  }: {
    list: string[];
    /** names of the instances of the algorithms of this usage */
    instances: string[];
    usage: 'property_validator' | 'node_validator' | 'link_validator';
    /** "validator", "node validator", "link validator" */
    noun?: string;
    bad?: (path: string) => boolean;
    readonly?: boolean;
    /** path of the list in the domain, e.g. "nodeTypes[2].validators": its issues are revealed on it */
    path: string;
  } = $props();
</script>

<div class="validators" data-path={path}>
  {#each list as v, k}
    <div class="vrow" data-path="{path}[{k}]">
      <select aria-label="{noun} instance" bind:value={list[k]} class:bad={bad(`${path}[${k}]`)} disabled={readonly}>
        <option value="">— validator —</option>
        {#if v && !instances.includes(v)}<option value={v}>{v} (unknown)</option>{/if}
        {#each instances as i (i)}<option value={i}>{i}</option>{/each}
      </select>
      {#if !readonly}
        <RowTools index={k} count={list.length} label="the validator" onmove={(delta) => moveItem(list, k, delta)} onremove={() => list.splice(k, 1)} />
      {/if}
    </div>
  {/each}
  {#if !list.length && readonly}<p class="hint">none</p>{/if}
  {#if !readonly}
    <button type="button" class="small" disabled={!instances.length} title={instances.length ? '' : `Create a ${usage} instance first (Algorithms)`} onclick={() => list.push('')}>+ {noun[0].toUpperCase() + noun.slice(1)}</button>
  {/if}
</div>

<style>
  .vrow {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    margin: 0.2rem 0;
  }
  .vrow select {
    width: auto;
    min-width: 11rem;
  }
</style>
