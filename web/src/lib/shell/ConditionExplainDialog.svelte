<script lang="ts">
  // Condition solver: the CEL formula of a condition of a run's world state, the value of each part of it and of the
  // blackboard variables it reads, against the change of the run. Mounted once at the shell root.
  import { engine, type ConditionExplanation, type ConditionTerm } from '../api';
  import { conditionExplainState, closeConditionExplain } from './conditionExplainState.svelte';

  let dialog = $state<HTMLDivElement>();
  let ex = $state<ConditionExplanation>();
  let failure = $state('');
  let loading = $state(false);

  $effect(() => {
    const req = conditionExplainState.current;
    ex = undefined;
    failure = '';
    if (!req) return;
    queueMicrotask(() => dialog?.focus());
    const ctl = new AbortController();
    loading = true;
    engine
      .explainCondition(req.processId, req.condition, ctl.signal)
      .then((r) => (ex = r))
      .catch((e) => {
        if (!ctl.signal.aborted) failure = e instanceof Error ? e.message : String(e);
      })
      .finally(() => (loading = false));
    return () => ctl.abort();
  });

  const inputs = $derived(Object.entries(ex?.inputs ?? {}));

  function shown(t: ConditionTerm): string {
    if (t.error) return t.error;
    if (t.skipped) return 'not evaluated';
    return t.value ?? '';
  }
</script>

<svelte:window onkeydown={(e) => conditionExplainState.current && e.key === 'Escape' && closeConditionExplain()} />

{#snippet node(t: ConditionTerm)}
  <li>
    <div class="term">
      <code class="text">{t.text}</code>
      <span class="val" class:yes={t.value === 'true'} class:no={t.value === 'false'} class:err={!!t.error} class:skip={t.skipped}>{shown(t)}</span>
    </div>
    {#if t.terms?.length}
      <ul>
        {#each t.terms as k, i (i)}{@render node(k)}{/each}
      </ul>
    {/if}
  </li>
{/snippet}

{#if conditionExplainState.current}
  {@const req = conditionExplainState.current}
  <div class="backdrop" role="presentation" onmousedown={closeConditionExplain}>
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-label="Condition {req.condition}"
      tabindex="-1"
      bind:this={dialog}
      onmousedown={(e) => e.stopPropagation()}
    >
      <header>
        <h3><code>{req.condition}</code></h3>
        {#if ex && !ex.error}
          <span class="val" class:yes={ex.value} class:no={!ex.value}>{ex.value ? 'true' : 'false'}</span>
        {:else if ex}
          <span class="val err">unknown</span>
        {/if}
        <button type="button" class="ghost" aria-label="Close" onclick={closeConditionExplain}>✕</button>
      </header>
      {#if loading}
        <p class="muted">Evaluating…</p>
      {:else if failure}
        <p class="error">{failure}</p>
      {:else if ex}
        {#if ex.waived}<p class="note">Declared established by a person: the run takes this value whatever the formula gives.</p>{/if}
        {#if ex.note}<p class="muted">{ex.note}</p>{/if}
        {#if ex.error}<p class="error">{ex.error}</p>{/if}
        {#if ex.expr}
          <h4>Formula</h4>
          <pre class="expr">{ex.expr}</pre>
          {#if ex.root}
            <h4>Evaluation</h4>
            <ul class="tree">{@render node(ex.root)}</ul>
          {/if}
          {#if inputs.length}
            <h4>Change context</h4>
            <dl>
              {#each inputs as [name, value] (name)}
                <dt><code>{name}</code></dt>
                <dd><code>{value}</code></dd>
              {/each}
            </dl>
          {/if}
        {/if}
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 98;
    display: grid;
    place-items: center;
    background: rgba(0, 0, 0, 0.4);
  }
  .dialog {
    width: min(720px, calc(100vw - 28px));
    max-height: calc(100vh - 40px);
    overflow: auto;
    padding: 1rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  h3 {
    margin: 0;
    flex: 1;
  }
  h4 {
    margin: 0.9rem 0 0.3rem;
    font-size: 0.8rem;
    text-transform: uppercase;
    color: var(--muted);
  }
  .muted,
  .note {
    color: var(--muted);
  }
  .error {
    color: var(--warn);
  }
  .expr {
    margin: 0;
    padding: 0.5rem 0.6rem;
    background: var(--neutral-soft);
    border-radius: var(--radius-sm);
    white-space: pre-wrap;
    word-break: break-word;
  }
  ul {
    list-style: none;
    margin: 0;
    padding-left: 1.1rem;
    border-left: 1px solid var(--border);
  }
  .tree {
    padding-left: 0;
    border-left: 0;
  }
  .term {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    justify-content: space-between;
    padding: 0.15rem 0;
  }
  .text {
    word-break: break-word;
  }
  .val {
    flex: none;
    max-width: 50%;
    overflow-wrap: anywhere;
    font-size: 0.78rem;
    font-weight: 700;
    border-radius: var(--radius-sm);
    padding: 0 0.4rem;
    background: var(--neutral-soft);
    color: var(--text);
  }
  .val.yes {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .val.no {
    background: var(--neutral-soft);
    color: var(--muted);
  }
  .val.err {
    background: var(--warn-soft);
    color: var(--warn);
  }
  .val.skip {
    font-weight: 400;
    font-style: italic;
    color: var(--muted);
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.25rem 0.8rem;
    margin: 0;
  }
  dd {
    margin: 0;
    max-height: 7rem;
    overflow: auto;
    word-break: break-all;
  }
</style>
