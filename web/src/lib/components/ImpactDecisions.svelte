<script lang="ts">
  // The decision history of a change impact (ADR 0152): its review status now, and, unfolded, every decision on it the
  // latest first (who, when, the comment, the review it was submitted in, the flow), the returns to proposed included.
  // An accepted verdict passed the validation rules on the draft as it stood then (the graph refuses one that fails
  // them), so the history says so rather than checking the impact again; a committed impact names its version.
  import { formatDate, type ChangeImpact } from '../api';
  import { committedAs, decisionHistory } from '../reviews';
  import StatusBadge from './StatusBadge.svelte';

  let { impact }: { impact: ChangeImpact } = $props();

  const history = $derived(decisionHistory(impact));
  const committed = $derived(committedAs(impact));
  const comment = (c: string) => (c === 'edited again' ? 'edited after the verdict: to review again' : c);
</script>

<div class="decisions">
  <span class="now">
    <StatusBadge status={impact.review} />
    {#if committed}<span class="hint">committed v{committed.version}</span>{/if}
  </span>
  {#if history.length}
    <details>
      <summary class="hint">{history.length} decision{history.length > 1 ? 's' : ''}{history[0].by ? ` · last by ${history[0].by}` : ''}</summary>
      <ol>
        {#each history as d, i (i)}
          <li class:superseded={d.superseded}>
            <span class="when hint">{formatDate(d.at)}</span>
            <StatusBadge status={d.status} />
            <span class="what">
              {#if d.by}<strong>{d.by}</strong>{/if}
              {#if d.comment}<span>"{comment(d.comment)}"</span>{/if}
              {#if d.reviewId}<span class="hint mono">{d.reviewId}</span>{/if}
              {#if d.flow}<span class="hint">on option {d.flow}</span>{/if}
              {#if d.checked}<span class="hint" title="The graph refuses an accept whose draft fails a validation rule: the rules held on the draft as it stood at this verdict">rules checked on the draft then</span>{/if}
              {#if d.superseded}<span class="hint">replaced by an adopted flow</span>{/if}
            </span>
          </li>
        {/each}
      </ol>
    </details>
  {/if}
</div>

<style>
  .decisions {
    display: grid;
    gap: 2px;
  }
  .now {
    display: flex;
    gap: 6px;
    align-items: baseline;
    flex-wrap: wrap;
  }
  summary {
    cursor: pointer;
  }
  ol {
    list-style: none;
    margin: 4px 0 0;
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    display: grid;
    gap: 4px;
  }
  li {
    display: grid;
    grid-template-columns: auto auto minmax(0, 1fr);
    gap: 8px;
    align-items: baseline;
  }
  li.superseded {
    opacity: 0.6;
  }
  .when {
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
  .what {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: baseline;
  }
  .mono {
    font-family: var(--mono);
  }
</style>
