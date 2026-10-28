<script lang="ts">
  // The transitions out of the current state of the change lifecycle (ADR 0058): what the definition says of each
  // gate, what the server judges of it for the caller now (allowed, or each check that blocks it and why), a Move
  // button with its confirmation, and the server's refusal when it says no (vetos, objectives).
  import type { DecisionPoint } from '../api';
  import { blockLabel, decisionLabel, type Refusal, type TransitionOffer } from '../changeTransition';

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
  /** the decided points to pick from: those the guard passes with, when the server judged it */
  const choices = (o: TransitionOffer) => (o.judgement ? pickable.filter((p) => o.judgement?.decisions.includes(p.id ?? '')) : pickable);
  const blocked = (o: TransitionOffer) => !!o.judgement && !o.judgement.allowed;
</script>

<div class="transitions">
  {#if !offers.length}
    <p class="hint">No transition leaves the state <code>{current}</code>.</p>
  {/if}
  {#each offers as o (o.name)}
    <div class="row" class:blocked={blocked(o)}>
      {#if o.judgement}
        <span class="verdict" class:ok={o.judgement.allowed} title={o.judgement.allowed ? 'You can take it now' : 'Blocked'}>{o.judgement.allowed ? '✓' : '✕'}</span>
      {/if}
      <button type="button" class="small" class:primary={!blocked(o)} disabled={!!busy || blocked(o) || (o.needsDecision && !decisionOf(o))} title={o.description || `Move to ${o.to}`} onclick={() => onmove(o, decisionOf(o))}>
        {busy === o.name ? 'Moving…' : `Move: ${o.name}`}
      </button>
      <span class="hint">→ <code>{o.to}</code></span>
      {#if o.needsDecision}
        <select bind:value={picked[o.name]} disabled={!!busy} aria-label="Decision point for {o.name}">
          <option value="">{choices(o).length ? 'Pick a decided point…' : 'No decided point available'}</option>
          {#each choices(o) as p (p.id)}<option value={p.id}>{decisionLabel(p)}</option>{/each}
        </select>
      {/if}
      {#if o.gate}<span class="hint gate" title={o.gate}>{o.gate}</span>{/if}
    </div>
    {#if o.judgement && !o.judgement.allowed}
      <ul class="blocks">
        {#each o.judgement.blocks as b, i (i)}
          <li><span class="kind">{blockLabel(b.kind)}</span> {b.reason}</li>
        {/each}
      </ul>
    {:else if o.judgement?.reserve.length}
      <p class="hint reserve">Goes with reserve of {o.judgement.reserve.join(', ')} (covered by a derogation).</p>
    {/if}
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
  .verdict {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.2rem;
    height: 1.2rem;
    border-radius: 50%;
    font-size: 0.75rem;
    font-weight: 700;
    color: var(--danger);
    background: color-mix(in srgb, var(--danger) 14%, transparent);
  }
  .verdict.ok {
    color: var(--ok);
    background: color-mix(in srgb, var(--ok) 16%, transparent);
  }
  .blocks {
    margin: 0 0 0.2rem 1.8rem;
    padding: 0;
    list-style: none;
    font-size: 0.88em;
    color: var(--muted);
  }
  .blocks .kind {
    display: inline-block;
    min-width: 4.6rem;
    font-weight: 600;
    color: var(--danger);
  }
  .reserve {
    margin: 0 0 0.2rem 1.8rem;
  }
  .refusal {
    color: var(--danger, #b3261e);
    font-size: 0.9em;
  }
</style>
