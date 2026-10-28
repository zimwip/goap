<script lang="ts">
  // The lifecycle a change follows (ADR 0058), as drawn in the lifecycle definition of its domain, with the state the
  // change is in and, when the server judged them, the transitions out of it the caller may take or what blocks them. The change tab loads the definition (it also feeds the transitions); shown when the change has one.
  import type { Snippet } from 'svelte';
  import type { Lifecycle } from '../api';
  import { lifecycleToForm } from '../methodologyForm';
  import { availableTransitions, type Judged } from '../changeTransition';
  import LifecyclePreview from './LifecyclePreview.svelte';

  let {
    lifecycle,
    current = '',
    definition,
    domain = '',
    loading = false,
    failure = '',
    judged,
    children,
  }: {
    lifecycle: string;
    current?: string;
    definition?: Lifecycle;
    domain?: string;
    loading?: boolean;
    failure?: string;
    /** the server's judgement of the transitions out of current, for the caller (GetChangeTransitions) */
    judged?: Judged;
    children?: Snippet;
  } = $props();

  const lc = $derived(definition ? lifecycleToForm(definition) : undefined);
  const transitions = $derived(availableTransitions(definition, current, judged));
  const allowed = $derived(transitions.filter((t) => !t.judgement || t.judgement.allowed));
</script>

<section class="card change-lifecycle">
  <div class="head">
    <strong>Lifecycle</strong>
    <code>{lifecycle}</code>
    {#if domain}<span class="hint">{domain}</span>{/if}
    <span class="grow"></span>
    {#if current}<span class="hint">state <code>{current}</code>{#if allowed.length} → {allowed.map((t) => t.name).join(', ')}{/if}</span>{/if}
  </div>
  {#if loading}
    <p class="hint">Loading…</p>
  {:else if failure}
    <p class="hint">{failure}</p>
  {:else if lc}
    <LifecyclePreview {lc} {current} {judged} />
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
