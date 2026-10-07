<script lang="ts">
  // The prompt of an LLM call: the system text, the messages sent and the answer. An engine call of a change is read from
  // the log of the change (ADR 0030, entries model.call); every other call from the gateway, which stores it (ADR 0089).
  // Mounted once at the shell root.
  import { decodeLogEntry, errorMessage, formatDuration, graph, int, isNotFound, models, type LLMCall, type ModelExchange } from '../api';
  import { sourceLabel } from '../tokenStats';
  import { behaviorsLine } from '../behaviors';
  import { modelExchangeState, closeModelExchange } from './modelExchangeState.svelte';

  let dialog = $state<HTMLDivElement>();
  let ex = $state<ModelExchange>();
  let failure = $state('');
  let loading = $state(false);
  // the ledger row the exchange belongs to: the gateway's answer carries it, the console's row stands for a log read
  let meta = $state<LLMCall>();
  // the global behaviours the gateway added to the instructions (ADR 0093): the ledger row's, or the log entry's
  const applied = $derived(behaviorsLine(ex?.behaviors ?? meta?.behaviors, ex?.behaviorTokens ?? meta?.behaviorTokens, ex?.behaviorsEstimated ?? meta?.behaviorTokensEstimated));
  // an engine call's log keeps the system text the engine built; the gateway's addition is named, not repeated
  const fromLog = $derived(modelExchangeState.current ? !('seq' in modelExchangeState.current) : false);

  $effect(() => {
    const req = modelExchangeState.current;
    ex = undefined;
    failure = '';
    meta = req?.meta;
    if (!req) return;
    queueMicrotask(() => dialog?.focus());
    const ctl = new AbortController();
    loading = true;
    const done = () => {
      if (!ctl.signal.aborted) loading = false;
    };
    if ('seq' in req) {
      models
        .getCallExchange(req.seq, ctl.signal)
        .then((r) => {
          meta = r.call ?? meta;
          ex = { system: r.system, messages: r.messages, response: r.response, truncated: r.truncated, behaviors: r.call?.behaviors, behaviorTokens: Number(r.call?.behaviorTokens ?? 0), behaviorsEstimated: !!r.call?.behaviorTokensEstimated };
        })
        .catch((e) => {
          if (ctl.signal.aborted) return;
          failure = isNotFound(e)
            ? 'The prompt of this call is no longer available: it was not stored (storage is off, or the call predates it) or it was purged with the ledger.'
            : errorMessage(e);
        })
        .finally(done);
    } else {
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
        .finally(done);
    }
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
      <div class="body">
      {#if meta}
        <p class="meta">
          {sourceLabel(meta.source)}{#if meta.subject} · {meta.subject}{/if} · {meta.alias ? `${meta.alias} → ` : ''}{meta.provider ? `${meta.provider}/` : ''}{meta.model ?? ''}
          · {int(meta.inputTokens)} in / {int(meta.outputTokens)} out{#if meta.durationMs} · {formatDuration(int(meta.durationMs))}{/if}
        </p>
        {#if meta.error}<p class="error">Call failed: {meta.error}</p>{/if}
      {/if}
      {#if loading}
        <p class="muted">Loading…</p>
      {:else if failure}
        <p class="error">{failure}</p>
      {:else if ex}
        {#if ex.truncated}<p class="note">A text was longer than what is kept and is cut.</p>{/if}
        {#if applied}
          <p class="note">
            Applied behaviours: {applied}.{#if fromLog} The system text below is what the run built; the gateway added these on top.{:else} The system text below is as sent, behaviours included.{/if}
          </p>
        {/if}
        {#if ex.system}{@render block('System', ex.system)}{/if}
        {#each ex.messages ?? [] as m, i (i)}
          {@render block(m.role ?? 'message', m.content ?? '')}
        {/each}
        {#if ex.response}{@render block('Answer', ex.response)}{/if}
      {/if}
      </div>
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
    display: flex;
    flex-direction: column;
    padding: 0;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  /* the title and the close button stay in view while the prompt scrolls */
  header {
    flex: none;
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.7rem 1rem;
    border-bottom: 1px solid var(--border);
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.3rem 1rem 1rem;
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
  .meta {
    margin: 0.3rem 0 0;
    color: var(--muted);
    font-size: 0.85rem;
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
