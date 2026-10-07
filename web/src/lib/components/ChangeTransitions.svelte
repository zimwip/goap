<script lang="ts">
  // The transitions out of the current state of the change lifecycle (ADR 0058): what the definition says of each
  // gate, a Move button with its confirmation, and the server's refusal when it says no (vetos, objectives).
  import type { DecisionPoint } from '../api';
  import { decisionLabel, type Refusal, type TransitionOffer } from '../changeTransition';

  let {
    current,
    offers,
    pickable,
    busy = '',
    refusal,
    onmove,
  }: {
    current: string;
    offers: TransitionOffer[];
    pickable: DecisionPoint[];
    busy?: string;
    refusal?: { transition: string; refusal: Refusal };
    onmove: (offer: TransitionOffer, decision?: DecisionPoint) => void;
  } = $props();

  let picked = $state<Record<string, string>>({});
  const decisionOf = (o: TransitionOffer) => pickable.find((p) => p.id === picked[o.name]);
</script>

<div class="transitions">
  {#if !offers.length}
    <p class="hint">No transition leaves the state <code>{current}</code>.</p>
  {/if}
  {#each offers as o (o.name)}
    <div class="row">
      <button type="button" class="small primary" disabled={!!busy || (o.needsDecision && !decisionOf(o))} title={o.description || `Move to ${o.to}`} onclick={() => onmove(o, decisionOf(o))}>
        {busy === o.name ? 'Moving…' : `Move: ${o.name}`}
      </button>
      <span class="hint">→ <code>{o.to}</code></span>
      {#if o.needsDecision}
        <select bind:value={picked[o.name]} disabled={!!busy} aria-label="Decision point for {o.name}">
          <option value="">{pickable.length ? 'Pick a decided point…' : 'No decided point available'}</option>
          {#each pickable as p (p.id)}<option value={p.id}>{decisionLabel(p)}</option>{/each}
        </select>
      {/if}
      {#if o.gate}<span class="hint gate" title={o.gate}>{o.gate}</span>{/if}
    </div>
    {#if refusal?.transition === o.name}
      <div class="refusal" role="alert">
        <strong>Refused.</strong> {refusal.refusal.message}
        {#if refusal.refusal.vetoed.length}<div>Vetos: {refusal.refusal.vetoed.join(', ')}</div>{/if}
        {#if refusal.refusal.unmet.length}<div>Objectives not met: {refusal.refusal.unmet.join(', ')}</div>{/if}
      </div>
    {/if}
  {/each}
</div>

<style>
  .transitions {
    margin-top: 0.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    flex-wrap: wrap;
  }
  .gate {
    max-width: 40rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .refusal {
    color: var(--danger, #b3261e);
    font-size: 0.9em;
  }
</style>
