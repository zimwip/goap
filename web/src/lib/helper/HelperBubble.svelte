<script lang="ts">
  // The contextual helper's bubble (ADR 0086): points at the field of the current proposal, shows the proposed value
  // and why, and lets the user accept it, reject the helper, or comment to refine. Ephemeral: see helper.svelte.ts.
  import { tick } from 'svelte';
  import { activeTab } from '../shell/tabs.svelte';
  import { valueText } from '../attributes';
  import { accept, closeHelper, comment, currentProposal, helper } from './helper.svelte';
  import { fieldOf } from './fields.svelte';
  import { place, type Placement } from './placement';

  let bubble = $state<HTMLDivElement>();
  let acceptBtn = $state<HTMLButtonElement>();
  let box = $state<HTMLTextAreaElement>();
  let draft = $state('');
  let pos = $state<Placement>({ mode: 'floating' });
  let returnFocus: Element | null = null;

  const proposal = $derived(currentProposal());
  const target = $derived(proposal ? fieldOf(proposal.fieldId, helper.tab) : undefined);
  const shown = $derived(proposal ? valueText(proposal.value) : '');
  const current = $derived(target ? valueText(target.spec.get()) : '');
  const count = $derived(helper.proposals.length);

  function reposition() {
    if (!bubble) return;
    const r = bubble.getBoundingClientRect();
    pos = place(target?.el.getBoundingClientRect(), { width: r.width, height: r.height }, window.innerWidth, window.innerHeight);
  }

  let frame = 0;
  function schedule() {
    if (!frame) frame = requestAnimationFrame(() => ((frame = 0), reposition()));
  }

  // follow scroll (any scrolling ancestor: capture), resize and the bubble's own size
  $effect(() => {
    if (!helper.open) return;
    window.addEventListener('scroll', schedule, { capture: true, passive: true });
    window.addEventListener('resize', schedule);
    const ro = bubble ? new ResizeObserver(schedule) : undefined;
    if (bubble) ro?.observe(bubble);
    return () => {
      window.removeEventListener('scroll', schedule, { capture: true });
      window.removeEventListener('resize', schedule);
      ro?.disconnect();
      if (frame) cancelAnimationFrame(frame);
      frame = 0;
    };
  });

  // point at the field of the proposal shown, and mark it
  $effect(() => {
    const el = target?.el;
    void [proposal, helper.loading, helper.error];
    void tick().then(reposition);
    if (!el) return;
    el.setAttribute('data-helper-target', '');
    el.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
    return () => el.removeAttribute('data-helper-target');
  });

  // keyboard focus goes in when the helper opens, and back where it was when it closes
  $effect(() => {
    if (!helper.open) return;
    returnFocus = document.activeElement;
    return () => {
      if (returnFocus instanceof HTMLElement && returnFocus.isConnected) returnFocus.focus();
      returnFocus = null;
    };
  });
  $effect(() => {
    if (!helper.open || helper.loading) return;
    void proposal;
    void tick().then(() => (acceptBtn ?? box)?.focus());
  });

  // the helper belongs to the tab it was opened on
  $effect(() => {
    if (helper.open && activeTab()?.id !== helper.tab) closeHelper();
  });

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      closeHelper();
    } else if (e.key === 'Tab' && bubble) {
      // keep the focus inside the bubble
      const items = [...bubble.querySelectorAll<HTMLElement>('button:not([disabled]), textarea')];
      if (!items.length) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (e.shiftKey && document.activeElement === first) (e.preventDefault(), last.focus());
      else if (!e.shiftKey && document.activeElement === last) (e.preventDefault(), first.focus());
    }
  }

  async function send() {
    const t = draft;
    if (!t.trim() || helper.loading) return;
    draft = '';
    await comment(t);
  }

  function boxKey(e: KeyboardEvent) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      void send();
    }
  }
</script>

{#if helper.open}
  <div
    class="bubble"
    class:floating={pos.mode === 'floating'}
    class:below={pos.mode === 'anchored' && pos.side === 'below'}
    class:above={pos.mode === 'anchored' && pos.side === 'above'}
    style:top={pos.mode === 'anchored' ? `${pos.top}px` : undefined}
    style:left={pos.mode === 'anchored' ? `${pos.left}px` : undefined}
    style:--arrow={pos.mode === 'anchored' ? `${pos.arrow}px` : undefined}
    role="dialog"
    aria-modal="false"
    aria-label="Assistant suggestion{target ? ` for ${target.spec.label}` : ''}"
    tabindex="-1"
    bind:this={bubble}
    onkeydown={keydown}
  >
    <div class="head">
      <strong>{target ? target.spec.label : 'Helper'}</strong>
      {#if count > 1}<span class="muted" aria-label="Proposal {helper.index + 1} of {count}">{helper.index + 1} / {count}</span>{/if}
    </div>

    <div class="body" aria-live="polite" aria-busy={helper.loading}>
      {#if helper.loading}
        <p class="muted">Thinking…</p>
      {:else}
        {#if helper.error}<div class="alert" role="alert">{helper.error}</div>{/if}
        {#if helper.note}<p class="note">{helper.note}</p>{/if}
        {#if proposal}
          <div class="value" data-testid="proposed-value">{shown === '' ? '(empty)' : shown}</div>
          {#if proposal.rationale}<p class="why">{proposal.rationale}</p>{/if}
          {#if current !== '' && current !== shown}<p class="muted current">Now: <span class="mono">{current}</span></p>{/if}
        {:else if !helper.error && !helper.note}
          <p class="muted">No proposal for this form. Say what you need below.</p>
        {/if}
      {/if}
    </div>

    <div class="actions">
      <button type="button" class="primary" bind:this={acceptBtn} disabled={!proposal || helper.loading} onclick={accept}>Accept</button>
      <button type="button" onclick={closeHelper}>Reject</button>
    </div>
    <div class="comment">
      <label class="muted" for="helper-comment">Comment</label>
      <textarea id="helper-comment" rows="2" bind:this={box} bind:value={draft} onkeydown={boxKey} placeholder="Ask for a change… (Ctrl+Enter to send)"></textarea>
      <button type="button" disabled={!draft.trim() || helper.loading} onclick={send}>Send</button>
    </div>
  </div>
{/if}

<style>
  .bubble {
    position: fixed;
    z-index: 80;
    width: min(360px, calc(100vw - 16px));
    max-height: min(60vh, 460px);
    overflow: auto;
    display: grid;
    gap: 0.5rem;
    padding: 0.7rem 0.8rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    font-size: 13px;
  }
  .bubble.floating {
    right: 16px;
    bottom: 40px;
  }
  /* the arrow points at the field */
  .bubble.below::before,
  .bubble.above::before {
    content: '';
    position: absolute;
    left: calc(var(--arrow, 24px) - 6px);
    width: 10px;
    height: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    transform: rotate(45deg);
  }
  .bubble.below::before {
    top: -6px;
    border-right: 0;
    border-bottom: 0;
  }
  .bubble.above::before {
    bottom: -6px;
    border-left: 0;
    border-top: 0;
  }
  .head {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .muted {
    color: var(--muted);
    margin: 0;
  }
  .value {
    padding: 0.4rem 0.5rem;
    background: var(--accent-soft, var(--bg));
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 10rem;
    overflow: auto;
  }
  .why,
  .note {
    margin: 0.3rem 0 0;
  }
  .current {
    margin-top: 0.3rem;
    overflow-wrap: anywhere;
  }
  .actions {
    display: flex;
    gap: 0.4rem;
  }
  .comment {
    display: grid;
    gap: 0.25rem;
  }
  .comment textarea {
    width: 100%;
    resize: vertical;
  }
  .comment button {
    justify-self: end;
  }
  :global([data-helper-target]) {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
</style>
