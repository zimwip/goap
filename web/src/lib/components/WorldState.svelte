<script lang="ts">
  // The world state of a run: the value of each condition. By default only the conditions that hold (and the ones
  // that could not be evaluated) are listed; "All" shows the full picture. Each condition says where its value
  // comes from: dynamic (inferred from the change impacts and the change) or set (a fact an action records).
  import type { Action, Condition } from '../api';
  import { loadRaw, save } from '../shell/storage';
  import { sourceOf, variablesOf } from '../conditionSources';
  import { openConditionExplain } from '../shell/conditionExplainState.svelte';

  let {
    processId = '',
    world = {},
    unknown = {},
    conditions = [],
    actions = [],
  }: {
    /** the run: a dynamic condition opens its solver (formula and values against the run's change) */
    processId?: string;
    world?: Record<string, boolean>;
    unknown?: Record<string, string>;
    /** definitions of the methodology (expression, description): the source indicator */
    conditions?: Condition[];
    /** actions of the methodology: which ones set a condition */
    actions?: Action[];
  } = $props();

  const KEY = 'goap.ide.world.all';
  let all = $state(loadRaw(KEY) === true);
  $effect(() => save(KEY, all));

  const defs = $derived(new Map(conditions.map((c) => [c.name ?? '', c])));
  const names = $derived([...new Set([...Object.keys(world), ...Object.keys(unknown)])].sort());
  const holding = $derived(names.filter((n) => world[n] || unknown[n] !== undefined));
  const shown = $derived(all ? names : holding);

  /** actions whose effects make the condition true */
  const setters = (name: string) => actions.filter((a) => a.effects?.[name] === true).map((a) => a.name ?? '');

  function tip(name: string): string {
    const d = defs.get(name);
    const src = sourceOf(d?.expr);
    const from = variablesOf(d?.expr).join(', ');
    const lines = [d?.description ?? '', d?.expr ? `Expression: ${d.expr}` : ''];
    if (src === 'dynamic') lines.push(`Dynamic: inferred from ${from}, re-evaluated at every cycle (it can turn false again).`);
    if (src === 'set') {
      const by = setters(name);
      lines.push(`Set: true once an action records the fact (${from})${by.length ? `; set by ${by.join(', ')}` : ''}.`);
    }
    return lines.filter(Boolean).join('\n');
  }
</script>

{#if names.length}
  <div class="bar" role="group" aria-label="Conditions shown">
    <button type="button" class="small" class:primary={!all} aria-pressed={!all} onclick={() => (all = false)}>True <span class="n">{holding.length}</span></button>
    <button type="button" class="small" class:primary={all} aria-pressed={all} onclick={() => (all = true)}>All <span class="n">{names.length}</span></button>
    {#if conditions.length}
      <span class="legend"><span class="src dynamic">dynamic</span> inferred from the impacts and the change (click for its solver) · <span class="src set">set</span> by an action</span>
    {/if}
  </div>
  {#if shown.length}
    <table>
      <thead><tr><th>Condition</th><th>Value</th><th>Source</th></tr></thead>
      <tbody>
        {#each shown as name (name)}
          {@const err = unknown[name]}
          {@const src = sourceOf(defs.get(name)?.expr)}
          {@const explain = src === 'dynamic' && processId !== ''}
          <tr
            title={tip(name)}
            class:explain
            tabindex={explain ? 0 : undefined}
            onclick={explain ? () => openConditionExplain(processId, name) : undefined}
            onkeydown={explain ? (e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), openConditionExplain(processId, name)) : undefined}
          >
            <td><code>{name}</code></td>
            <td>
              {#if err !== undefined}
                <span class="v unknown" title={err}>unknown</span>
                <span class="err">{err}</span>
              {:else if world[name]}
                <span class="v yes">true</span>
              {:else}
                <span class="v no">false</span>
              {/if}
            </td>
            <td>
              {#if src === 'dynamic'}
                <span class="src dynamic">dynamic</span>
              {:else if src === 'set'}
                {@const by = setters(name)}
                <span class="src set">set</span>{#if by.length}<span class="by"> by {by.join(', ')}</span>{/if}
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="empty">No condition holds yet.</p>
  {/if}
{:else}
  <p class="empty">World state not yet evaluated.</p>
{/if}

<style>
  .bar {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    flex-wrap: wrap;
    margin-bottom: 0.4rem;
  }
  .n {
    opacity: 0.75;
    margin-left: 0.2rem;
  }
  .legend {
    color: var(--muted);
    font-size: 0.8rem;
    margin-left: 0.5rem;
  }
  tbody tr {
    cursor: help;
  }
  tbody tr.explain {
    cursor: pointer;
  }
  tbody tr.explain:hover {
    background: var(--neutral-soft);
  }
  .v {
    display: inline-block;
    min-width: 4.2rem;
    text-align: center;
    font-size: 0.78rem;
    font-weight: 700;
    border-radius: var(--radius-sm);
    padding: 0 0.4rem;
  }
  .yes {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .no {
    background: var(--neutral-soft);
    color: var(--muted);
  }
  .unknown {
    background: var(--warn-soft);
    color: var(--warn);
  }
  .err {
    margin-left: 0.5rem;
    color: var(--muted);
    font-size: 0.8rem;
  }
  .src {
    display: inline-block;
    font-size: 0.72rem;
    font-weight: 600;
    border-radius: 999px;
    padding: 0 0.45rem;
    border: 1px solid currentColor;
  }
  .src.dynamic {
    color: var(--accent);
  }
  .src.dynamic::before {
    content: '⟳ ';
  }
  .src.set {
    color: var(--muted);
  }
  .src.set::before {
    content: '● ';
  }
  .by {
    color: var(--muted);
    font-size: 0.8rem;
  }
</style>
