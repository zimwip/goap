<script lang="ts">
  // The prompt of an LLM call: the system text, the messages sent and the answer, read from the log of the change
  // (ADR 0030, entries model.call). Mounted once at the shell root.
  import { decodeLogEntry, errorMessage, graph, type ModelExchange } from '../api';
  import { modelExchangeState, closeModelExchange } from './modelExchangeState.svelte';

  let dialog = $state<HTMLDivElement>();
  let ex = $state<ModelExchange>();
  let failure = $state('');
  let loading = $state(false);

  $effect(() => {
    const req = modelExchangeState.current;
    ex = undefined;
    failure = '';
    if (!req) return;
    queueMicrotask(() => dialog?.focus());
    const ctl = new AbortController();
    loading = true;
    graph
      .listChangeLog({ changeId: req.changeId, types: ['model.call'], processIds: [req.processId] }, ctl.signal)
      .then((r) => {
        const found = (r.entries ?? []).map((l) => decodeLogEntry<ModelExchange>(l)).find((x) => x.step === req.step && x.call === req.call);
        if (found) ex = found;
        else failure = 'The prompt of this call was not recorded (the call predates prompt recording).';
      })
      .catch((e) => {
        if (!ctl.signal.aborted) failure = errorMessage(e);
      })
      .finally(() => {
        if (!ctl.signal.aborted) loading = false;
      });
    return () => ctl.abort();
  });

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      /* clipboard unavailable: the text stays selectable */
    }
  }
</script>

<svelte:window onkeydown={(e) => modelExchangeState.current && e.key === 'Escape' && closeModelExchange()} />

{#snippet block(title: string, text: string)}
  <h4>
    {title}
    <button type="button" class="ghost small" onclick={() => copy(text)}>Copy</button>
  </h4>
  <pre class="text">{text}</pre>
{/snippet}

{#if modelExchangeState.current}
  {@const req = modelExchangeState.current}
  <div class="backdrop" role="presentation" onmousedown={closeModelExchange}>
    <div class="dialog" role="dialog" aria-modal="true" aria-label="Prompt {req.label}" tabindex="-1" bind:this={dialog} onmousedown={(e) => e.stopPropagation()}>
      <header>
        <h3>Prompt · {req.label}</h3>
        <button type="button" class="ghost" aria-label="Close" onclick={closeModelExchange}>✕</button>
      </header>
      {#if loading}
        <p class="muted">Loading…</p>
      {:else if failure}
        <p class="error">{failure}</p>
      {:else if ex}
        {#if ex.truncated}<p class="note">A text was longer than the log keeps and is cut.</p>{/if}
        {#if ex.system}{@render block('System', ex.system)}{/if}
        {#each ex.messages ?? [] as m, i (i)}
          {@render block(m.role ?? 'message', m.content ?? '')}
        {/each}
        {#if ex.response}{@render block('Answer', ex.response)}{/if}
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
    width: min(900px, calc(100vw - 28px));
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
    display: flex;
    align-items: center;
    gap: 0.6rem;
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
  .text {
    margin: 0;
    padding: 0.5rem 0.6rem;
    max-height: 40vh;
    overflow: auto;
    background: var(--neutral-soft);
    border-radius: var(--radius-sm);
    white-space: pre-wrap;
    word-break: break-word;
  }
</style>
