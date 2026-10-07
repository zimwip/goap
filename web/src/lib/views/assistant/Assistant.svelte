<script lang="ts">
  // The conversational assistant (ADR 0087): one conversation view, shown as a tab or as the compact floating panel.
  import { tick } from 'svelte';
  import Icon from '../../shell/Icon.svelte';
  import VoiceButton from '../../voice/VoiceButton.svelte';
  import { openSettings } from '../../shell/settingsState.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { assistantEnabled, ASSISTANT_OFF } from '../../assistant/enabled';
  import { actionChipLabel, repeatable, runAction } from '../../assistant/actions';
  import { isCard } from '../../assistant/proposal';
  import { captureSelectionNow } from '../../assist/capture';
  import ProposalCard from './ProposalCard.svelte';
  import { examplePrompts, loadExamples } from '../../assistant/examples';
  import {
    assistant,
    attachView,
    currentConversation,
    deleteConversation,
    isPending,
    loadConversations,
    MAX_TEXT_BYTES,
    newConversation,
    openConversation,
    renameConversation,
    retry,
    send,
  } from '../../stores/assistant.svelte';
  import { project, loadApplicable } from '../../stores/project.svelte';

  let {
    mode = 'tab',
    onclose,
    onexpand,
  }: { mode?: 'panel' | 'tab'; onclose?: () => void; onexpand?: () => void } = $props();

  let input = $state<HTMLTextAreaElement>();
  let scroller = $state<HTMLDivElement>();
  let renaming = $state(false);
  let renameText = $state('');
  const enabled = $derived(assistantEnabled());
  const pending = $derived(isPending());
  const current = $derived(currentConversation());
  const examples = $derived(examplePrompts());

  // polling runs while a view is shown
  $effect(() => {
    if (!enabled) return;
    return attachView();
  });

  // the examples are those of the active project
  $effect(() => {
    if (!enabled) return;
    void project.current;
    void loadApplicable().then(loadExamples);
  });

  $effect(() => {
    if (assistant.focus) void tick().then(() => input?.focus());
  });

  // scroll to the newest message
  $effect(() => {
    void assistant.messages.length;
    void pending;
    void tick().then(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
  });

  async function submit(e?: SubmitEvent) {
    e?.preventDefault();
    const text = assistant.draft;
    if (await send(text)) assistant.draft = '';
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      void submit();
    }
  }

  function appendTranscript(text: string) {
    const d = assistant.draft.trimEnd();
    assistant.draft = d ? `${d} ${text}` : text;
    input?.focus();
  }

  function pick(e: Event) {
    const id = (e.currentTarget as HTMLSelectElement).value;
    if (!id) newConversation();
    else void openConversation(id);
  }

  function startRename() {
    renameText = current?.title ?? '';
    renaming = true;
  }

  async function commitRename() {
    renaming = false;
    if (current && renameText.trim() && renameText.trim() !== current.title) await renameConversation(current.id, renameText);
  }

  async function remove() {
    if (!current) return;
    if (await confirmDialog({ message: `Delete the conversation "${current.title || 'Untitled'}"?`, confirmLabel: 'Delete', danger: true })) await deleteConversation(current.id);
  }
</script>

<div class="assistant {mode}" data-assistant>
  <div class="bar">
    <strong class="title"><Icon name="chat" size={15} /> Assistant</strong>
    <span class="grow"></span>
    {#if enabled}
      <button type="button" class="small" onclick={newConversation} title="New conversation" aria-label="New conversation">
        <Icon name="plus" size={12} />{#if mode === 'tab'} New conversation{/if}
      </button>
    {/if}
    {#if mode === 'panel' && onexpand}
      <button type="button" class="ghost small" title="Open in a tab" aria-label="Open in a tab" onclick={onexpand}><Icon name="external" size={13} /></button>
    {/if}
    {#if mode === 'panel' && onclose}
      <button type="button" class="ghost small" title="Close (Esc)" aria-label="Close the assistant" onclick={onclose}><Icon name="x" size={13} /></button>
    {/if}
  </div>

  {#if !enabled}
    <div class="off" role="status">
      <h2>The assistant is not available</h2>
      <p class="muted">{ASSISTANT_OFF}. An administrator can give the <span class="mono">assistant</span> alias a model in the model catalog.</p>
      <button type="button" class="link" onclick={() => openSettings('catalog')}>Open the model catalog</button>
    </div>
  {:else}
    <div class="picker">
      <label class="sr" for="as-conv-{mode}">Conversation</label>
      <select id="as-conv-{mode}" value={assistant.currentId} onchange={pick}>
        <option value="">New conversation</option>
        {#each assistant.conversations as c (c.id)}
          <option value={c.id}>{c.title || 'Untitled'}</option>
        {/each}
      </select>
      {#if current}
        {#if renaming}
          <!-- svelte-ignore a11y_autofocus -->
          <input
            class="rename"
            bind:value={renameText}
            aria-label="Conversation title"
            autofocus
            onkeydown={(e) => {
              if (e.key === 'Enter') void commitRename();
              else if (e.key === 'Escape') {
                e.stopPropagation();
                renaming = false;
              }
            }}
            onblur={() => void commitRename()}
          />
        {:else}
          <button type="button" class="ghost small" onclick={startRename}>Rename</button>
        {/if}
        <button type="button" class="ghost small" onclick={remove} aria-label="Delete the conversation" title="Delete the conversation"><Icon name="trash" size={13} /></button>
      {/if}
    </div>

    <div class="scroll" bind:this={scroller}>
      {#if assistant.loading && !assistant.messages.length && assistant.currentId}
        <p class="muted">Loading…</p>
      {:else if !assistant.messages.length}
        <div class="home">
          <h2>Hello, what can I do for you?</h2>
          <p class="muted">Ask what a methodology is for, or describe what you need: the assistant can start a change or take you to one.</p>
          {#if examples.length}
            <div class="sugg">
              {#each examples as ex (ex)}
                <button type="button" class="suggestion" onclick={() => ((assistant.draft = ex), input?.focus())}>« {ex} »</button>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        <div class="convo" role="log" aria-live="polite" aria-label="Conversation">
          {#each assistant.messages as m (m.id)}
            {#if m.role === 'user'}
              <div class="bubble me">{m.text}</div>
            {:else if m.status === 'pending'}
              <div class="bubble bot pending" aria-busy="true"><span class="dots" aria-hidden="true"><i></i><i></i><i></i></span><span class="sr">The assistant is answering</span></div>
            {:else if m.status === 'error'}
              <div class="bubble bot err" role="alert">
                <span>{m.error || 'The assistant could not answer.'}</span>
                <button type="button" class="small" onclick={() => void retry(m.id)} disabled={assistant.sending}>Retry</button>
              </div>
            {:else}
              <div class="bubble bot">
                {#if m.text}<div class="text">{m.text}</div>{/if}
                {#if m.actions?.length}
                  <div class="chips">
                    {#each m.actions as a, i (i)}
                      {#if !isCard(a) && actionChipLabel(a)}
                        {#if repeatable(a)}
                          <button type="button" class="chip" title="Do it again" onclick={() => void runAction(a)}>{actionChipLabel(a)}</button>
                        {:else}
                          <span class="chip passive">{actionChipLabel(a)}</span>
                        {/if}
                      {/if}
                    {/each}
                  </div>
                  {#each m.actions as a, i (i)}
                    {#if isCard(a)}<ProposalCard message={m} index={i} />{/if}
                  {/each}
                {/if}
              </div>
            {/if}
          {/each}
        </div>
      {/if}
    </div>

    {#if assistant.error}
      <div class="alert err-line" role="alert">
        {assistant.error}
        {#if assistant.currentId}<button type="button" class="link" onclick={() => void openConversation(assistant.currentId, true)}>Reload</button>{:else}<button type="button" class="link" onclick={() => void loadConversations()}>Reload</button>{/if}
      </div>
    {/if}

    <form class="compose" onsubmit={submit}>
      <textarea
        bind:this={input}
        bind:value={assistant.draft}
        rows={mode === 'panel' ? 2 : 2}
        placeholder="Your message… (Enter to send, Shift+Enter for a new line)"
        aria-label="Your message"
        maxlength={MAX_TEXT_BYTES}
        onkeydown={keydown}
        onfocus={captureSelectionNow}
        data-no-pin
      ></textarea>
      <VoiceButton ontranscript={appendTranscript} />
      <button type="submit" class="primary" disabled={assistant.sending || pending || !assistant.draft.trim()}>
        <Icon name="send" size={14} />
        {assistant.sending ? 'Sending…' : 'Send'}
      </button>
    </form>
  {/if}
</div>

<style>
  .assistant {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    font-size: 14px;
  }
  .assistant.panel {
    font-size: 13px;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid var(--border);
    flex: none;
  }
  .title {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }
  .tab .title {
    font-size: 1rem;
  }
  .bar button,
  .picker button {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }
  .grow {
    flex: 1;
  }
  .picker {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid var(--border);
    flex: none;
  }
  .picker select {
    flex: 1;
    min-width: 0;
  }
  .rename {
    min-width: 0;
    width: 10rem;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
  .off {
    padding: 1.2rem;
    display: grid;
    gap: 0.5rem;
    justify-items: start;
  }
  .scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.8rem;
  }
  .tab .scroll {
    padding: 1.2rem max(1rem, calc((100% - 820px) / 2));
  }
  .home h2 {
    font-size: 1.1rem;
  }
  .muted {
    color: var(--muted);
  }
  .sugg {
    display: grid;
    gap: 0.25rem;
    margin-top: 0.8rem;
  }
  .suggestion {
    text-align: left;
    font-weight: 400;
    font-style: italic;
    background: var(--accent-soft);
    border-color: transparent;
    color: var(--accent);
    white-space: normal;
  }
  .convo {
    display: grid;
    gap: 0.6rem;
  }
  .bubble {
    max-width: 92%;
    padding: 0.45rem 0.7rem;
    border-radius: 12px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .bubble.me {
    justify-self: end;
    background: var(--accent);
    color: var(--accent-text);
    border-top-right-radius: 4px;
  }
  .bubble.bot {
    justify-self: start;
    background: var(--surface-2);
    border-top-left-radius: 4px;
  }
  .bubble.err {
    background: var(--danger-soft);
    color: var(--danger);
    display: flex;
    gap: 0.6rem;
    align-items: center;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    margin-top: 0.4rem;
  }
  .chip {
    font-size: 0.9em;
    background: var(--accent-soft);
    border-color: transparent;
    color: var(--accent);
    border-radius: 999px;
    padding: 0.1rem 0.6rem;
  }
  .chip.passive {
    background: var(--neutral-soft);
    color: var(--muted);
    cursor: default;
  }
  .dots {
    display: inline-flex;
    gap: 4px;
  }
  .dots i {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--muted);
    animation: as-blink 1.2s infinite ease-in-out;
  }
  .dots i:nth-child(2) {
    animation-delay: 0.2s;
  }
  .dots i:nth-child(3) {
    animation-delay: 0.4s;
  }
  @keyframes as-blink {
    0%,
    80%,
    100% {
      opacity: 0.25;
    }
    40% {
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .dots i {
      animation: none;
    }
  }
  .err-line {
    margin: 0 0.6rem;
    flex: none;
  }
  .compose {
    display: flex;
    gap: 0.5rem;
    align-items: flex-end;
    padding: 0.5rem 0.6rem;
    border-top: 1px solid var(--border);
    flex: none;
  }
  .tab .compose {
    padding: 0.6rem max(1rem, calc((100% - 820px) / 2));
  }
  .compose textarea {
    flex: 1;
    min-width: 0;
    resize: none;
    min-height: 2.6rem;
  }
  .compose button {
    justify-content: center;
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }
</style>
