<script lang="ts">
  // The card of a proposal of the assistant (ADR 0090, 0092): an agent to launch, or a change a screen tool would
  // make, with Accept / Reject. Nothing happens before the person decides; an accepted screen tool is applied through
  // the screen's own edit path. The element a write points at is outlined while the card is hovered or focused.
  import type { ConversationMessage, UiToolAction } from '../../api';
  import { cardView } from '../../assistant/proposal';
  import { highlightTarget, hasTool, targetOfCall } from '../../assist/registry.svelte';
  import { decide, proposalKey, proposals, retryApplied } from '../../stores/proposals.svelte';
  import AgentRunStatus from './AgentRunStatus.svelte';

  let { message, index }: { message: ConversationMessage; index: number } = $props();

  const action = $derived(message.actions?.[index]);
  const key = $derived(proposalKey(message, index));
  const ui = $derived(action?.type === 'ui_tool' ? (action as UiToolAction) : undefined);
  const registered = $derived(ui ? hasTool(ui.tool) : false);
  const target = $derived(ui ? ui.target || targetOfCall(ui.tool, (ui.args ?? {}) as Record<string, unknown>) || '' : '');
  const view = $derived(action ? cardView(action, { busy: !!proposals.busy[key], registered, target }) : undefined);
  const note = $derived(proposals.notes[key] ?? '');
  const busy = $derived(!!proposals.busy[key]);

  let release = () => {};
  const point = () => {
    release();
    release = highlightTarget(view?.target);
  };
  const unpoint = () => {
    release();
    release = () => {};
  };
  $effect(() => unpoint);
</script>

{#if view}
  <section
    class="card {view.status}"
    role="group"
    aria-label="{view.kind === 'start_agent' ? 'Proposal to run an agent' : 'Proposed change'}: {view.title}"
    onmouseenter={point}
    onmouseleave={unpoint}
    onfocusin={point}
    onfocusout={unpoint}
  >
    <header>
      <strong class="title">{view.title}</strong>
      <span class="status" aria-live="polite">{view.statusLabel}</span>
    </header>
    {#if view.rationale}<p class="why">{view.rationale}</p>{/if}
    {#if view.details.length}
      <dl>
        {#each view.details as d (d.label)}
          <dt>{d.label}</dt>
          <dd>{d.value}</dd>
        {/each}
      </dl>
    {/if}
    {#if view.status === 'started'}<AgentRunStatus processId={view.processId} changeId={view.changeId} />{/if}
    {#if view.error}<p class="err" role="alert">{view.error}</p>{/if}
    {#if note}<p class="err" role="alert">{note}</p>{/if}
    {#if view.status === 'unapplied'}
      <p class="hint">You accepted this, but its outcome was never recorded: it may not have been applied.</p>
      {#if view.retryBlocked}<p class="hint">{view.retryBlocked}</p>{/if}
    {/if}
    {#if view.canDecide || view.canRetry || (busy && view.status === 'proposed')}
      <div class="row">
        {#if view.canRetry}
          <button type="button" class="small primary" disabled={busy} onclick={() => void retryApplied(message, index, registered)}>Retry</button>
        {:else}
          <button type="button" class="small primary" disabled={!view.canDecide} onclick={() => void decide(message, index, 'accept')}>{busy ? 'Working…' : view.acceptLabel}</button>
          <button type="button" class="small" disabled={!view.canDecide} onclick={() => void decide(message, index, 'reject')}>Reject</button>
        {/if}
      </div>
    {/if}
  </section>
{/if}

<style>
  .card {
    margin-top: 0.4rem;
    padding: 0.5rem 0.65rem;
    border: 1px solid var(--border);
    border-left: 3px solid var(--accent);
    border-radius: 8px;
    background: var(--surface);
    white-space: normal;
  }
  .card.done,
  .card.started {
    border-left-color: var(--ok);
  }
  .card.failed {
    border-left-color: var(--danger);
  }
  .card.rejected,
  .card.unapplied {
    border-left-color: var(--muted);
  }
  header {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
    align-items: baseline;
    flex-wrap: wrap;
  }
  .status {
    font-size: 0.85em;
    color: var(--muted);
  }
  .why {
    margin: 0.25rem 0;
    font-style: italic;
    color: var(--muted);
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.1rem 0.6rem;
    margin: 0.3rem 0;
    font-size: 0.92em;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .err {
    margin: 0.3rem 0 0;
    color: var(--danger);
  }
  .hint {
    margin: 0.3rem 0 0;
    color: var(--muted);
    font-size: 0.9em;
  }
  .row {
    display: flex;
    gap: 0.4rem;
    margin-top: 0.45rem;
  }
</style>
