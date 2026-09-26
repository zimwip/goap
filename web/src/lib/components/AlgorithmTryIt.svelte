<script lang="ts">
  // "Try it": runs an algorithm (the unsaved code of the editor) on a sample input, on the
  // registry, without saving anything. The parameter values come from an instance, or are set here.
  import { registry, errorMessage, type RunAlgorithmResponse } from '../api';
  import { algorithmFromForm, sampleInput, type AlgorithmForm } from '../algorithmForm';
  import AlgorithmParamValues from './AlgorithmParamValues.svelte';

  let {
    algorithm,
    values,
  }: {
    algorithm: AlgorithmForm;
    /** parameter values of an instance; without them the panel offers its own form */
    values?: Record<string, unknown>;
  } = $props();

  let own = $state<Record<string, unknown>>({});
  let inputText = $state('');
  let touched = $state(false);
  let busy = $state(false);
  let error = $state('');
  let result = $state<RunAlgorithmResponse>();

  $effect(() => {
    const t = algorithm.type;
    if (!touched) inputText = sampleInput(t);
  });

  async function run() {
    error = '';
    result = undefined;
    let input: Record<string, unknown>;
    try {
      input = JSON.parse(inputText || '{}') as Record<string, unknown>;
    } catch (e) {
      error = `Sample input is not valid JSON: ${(e as Error).message}`;
      return;
    }
    busy = true;
    try {
      result = await registry.runAlgorithm(algorithmFromForm(algorithm), values ?? own, input);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<section class="card tryit" data-testid="try-it">
  <h3>Try it</h3>
  <p class="hint">Runs the code as it is in the editor (saved or not) on a sample input. Nothing is stored.</p>
  {#if !values && algorithm.params.length}
    <h4>Parameter values</h4>
    <AlgorithmParamValues params={algorithm.params} bind:values={own} idPrefix="try" />
  {/if}
  <div class="field">
    <label for="try-input">Sample input <span class="hint">(JSON)</span></label>
    <textarea id="try-input" class="mono" rows="9" spellcheck="false" bind:value={inputText} oninput={() => (touched = true)}></textarea>
  </div>
  <div class="actions">
    <button type="button" class="primary" disabled={busy} onclick={run}>{busy ? 'Running…' : 'Run'}</button>
    {#if touched}
      <button type="button" class="ghost" onclick={() => { touched = false; inputText = sampleInput(algorithm.type); }}>Reset the sample</button>
    {/if}
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if result}
    <div class="result" data-testid="try-result">
      {#if result.error}
        <div class="alert">Script error: {result.error}</div>
      {:else if result.ok}
        <div class="alert ok">Accepted.</div>
      {:else}
        <div class="alert warn">Rejected:</div>
        <ul>{#each result.failures ?? [] as f}<li>{f}</li>{/each}</ul>
      {/if}
      {#if result.set && Object.keys(result.set).length}
        <h4>Properties set</h4>
        <pre class="mono">{JSON.stringify(result.set, null, 2)}</pre>
      {/if}
      {#if result.unset?.length}<p>Properties removed: <code>{result.unset.join(', ')}</code></p>{/if}
      {#if result.logs?.length}
        <h4>Logs</h4>
        <pre class="mono">{result.logs.join('\n')}</pre>
      {/if}
    </div>
  {/if}
</section>

<style>
  h4 {
    margin: 0.5rem 0 0.2rem;
    font-size: 0.85rem;
    color: var(--muted);
  }
  .actions {
    display: flex;
    gap: 0.4rem;
  }
  .result {
    margin-top: 0.5rem;
  }
  pre {
    margin: 0;
    padding: 0.4rem 0.6rem;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
    overflow: auto;
    font-size: 0.82rem;
  }
  ul {
    margin: 0.2rem 0 0 1.2rem;
    padding: 0;
  }
</style>
