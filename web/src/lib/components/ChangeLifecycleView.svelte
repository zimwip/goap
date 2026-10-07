<script lang="ts">
  // The lifecycle a change follows (ADR 0058), as drawn in the lifecycle definition of its domain, with the state the
  // change is in. The change tab loads the definition (it also feeds the transitions); shown when the change has one.
  import type { Snippet } from 'svelte';
  import type { Lifecycle } from '../api';
  import { lifecycleToForm } from '../methodologyForm';
  import { availableTransitions } from '../changeTransition';
  import LifecyclePreview from './LifecyclePreview.svelte';

  let {
    lifecycle,
    current = '',
    definition,
    domain = '',
    loading = false,
    failure = '',
    children,
  }: { lifecycle: string; current?: string; definition?: Lifecycle; domain?: string; loading?: boolean; failure?: string; children?: Snippet } = $props();

  const lc = $derived(definition ? lifecycleToForm(definition) : undefined);
  const transitions = $derived(availableTransitions(definition, current));
</script>

<section class="card change-lifecycle">
  <div class="head">
    <strong>Lifecycle</strong>
    <code>{lifecycle}</code>
    {#if domain}<span class="hint">{domain}</span>{/if}
    <span class="grow"></span>
    {#if current}<span class="hint">state <code>{current}</code>{#if transitions.length} → {transitions.map((t) => t.name).join(', ')}{/if}</span>{/if}
  </div>
  {#if loading}
    <p class="hint">Loading…</p>
  {:else if failure}
    <p class="hint">{failure}</p>
  {:else if lc}
    <LifecyclePreview {lc} {current} />
  {/if}
  {@render children?.()}
</section>

<style>
  .head {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
  }
  .grow {
    flex: 1;
  }
</style>
