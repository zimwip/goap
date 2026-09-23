<script lang="ts">
  // Conversational assistant for non-specialists: presents the available
  // agents, plain-language requests, live follow-up.
  import { tick } from 'svelte';
  import Icon from '../../shell/Icon.svelte';
  import Popover from '../../shell/Popover.svelte';
  import AssistantRun from './AssistantRun.svelte';
  import {
    conversation,
    assistantUi,
    loadCatalog,
    agentCards,
    agentLabel,
    sendRequest,
    newConversation,
  } from '../../stores/assistant.svelte';
  import { baselines, refreshBaselines, methodologies } from '../../stores/catalog.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { formatDate, shortId } from '../../api';

  let { mode = 'tab' }: { mode?: 'panel' | 'tab' } = $props();

  let input = $state<HTMLTextAreaElement>();
  let scroller = $state<HTMLDivElement>();
  let settingsOpen = $state(false);
  let loadingCatalog = $state(true);

  $effect(() => {
    loadingCatalog = true;
    void loadCatalog().finally(() => (loadingCatalog = false));
    if (!baselines.loaded) void refreshBaselines();
  });

  $effect(() => {
    if (assistantUi.focus) input?.focus();
  });

  const cards = $derived(agentCards());
  const threads = $derived(conversation.threads);

  // Scroll to the bottom on every new request.
  $effect(() => {
    void threads.length;
    void tick().then(() => scroller?.scrollTo({ top: scroller.scrollHeight, behavior: 'smooth' }));
  });

  async function send(e?: SubmitEvent) {
    e?.preventDefault();
    await sendRequest(conversation.draft);
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  }

  function suggest(text: string) {
    conversation.draft = text;
    input?.focus();
  }

  function reset() {
    if (threads.length && !confirm('Start a new conversation? The current history will be erased.')) return;
    newConversation();
  }

  const sortedBaselines = $derived([...baselines.items].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? '')));
</script>

<div class="assistant {mode}">
  <div class="bar">
    <strong class="title"><Icon name="chat" size={15} /> Assistant</strong>
    <span class="grow"></span>
    {#if mode === 'panel'}
      <button type="button" class="ghost small" title="Open in a tab" aria-label="Open in a tab" onclick={() => openTab({ kind: 'assistant', params: {} }, { pin: true })}>
        <Icon name="external" size={13} />
      </button>
    {/if}
    <div class="settings">
      <button type="button" class="ghost small" title="Settings" aria-label="Settings" aria-expanded={settingsOpen} onclick={() => (settingsOpen = !settingsOpen)}>
        <Icon name="settings" size={13} />
      </button>
      <Popover bind:open={settingsOpen} label="Assistant settings" align="right" placement="below" width="280px">
        <div class="set">
          <label for="as-base">Working baseline</label>
          <select id="as-base" bind:value={conversation.baselineId}>
            <option value="">Most recent</option>
            {#each sortedBaselines as b (b.id)}<option value={b.id}>{b.name || shortId(b.id)} — {formatDate(b.createdAt)}</option>{/each}
          </select>
        </div>
      </Popover>
    </div>
    <button type="button" class="small" onclick={reset} title="New conversation" aria-label="New conversation">
      <Icon name="plus" size={12} />{#if mode === 'tab'} New conversation{:else} New{/if}
    </button>
  </div>

  <div class="scroll" bind:this={scroller}>
    {#if !threads.length}
      <div class="home">
        <h2>Hello, what can I do for you?</h2>
        <p class="muted">Describe what you need in your own words: the assistant picks the right agent and keeps you informed.</p>
        {#if loadingCatalog && !cards.length}
          <p class="muted">Loading agents…</p>
        {:else if methodologies.error}
          <div class="alert">{methodologies.error}</div>
        {:else if !cards.length}
          <p class="muted">No agent is available at the moment.</p>
        {/if}
        <div class="cards">
          {#each cards as c (`${c.methodology}/${c.agent.name}`)}
            <article class="card agent">
              <h3><Icon name="bot" size={15} /> {c.implicit ? agentLabel(c.methodology, '') : agentLabel(c.methodology, c.agent.name)}</h3>
              {#if c.agent.description}<p class="muted desc">{c.agent.description}</p>{/if}
              {#if c.examples.length}
                <div class="sugg">
                  {#each c.examples as ex (ex)}
                    <button type="button" class="suggestion" onclick={() => suggest(ex)}>« {ex} »</button>
                  {/each}
                </div>
              {/if}
            </article>
          {/each}
        </div>
      </div>
    {:else}
      <div class="convo">
        {#each threads as t, i (t.id)}
          <div class="thread">
            <div class="bubble me">{t.text}</div>
            {#if t.processId}
              <AssistantRun processId={t.processId} live={i === threads.length - 1} />
            {:else if t.error}
              <div class="bubble bot err">I could not start the request: {t.error}</div>
            {:else}
              <div class="bubble bot">…</div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </div>

  <form class="compose" onsubmit={send}>
    <textarea
      bind:this={input}
      bind:value={conversation.draft}
      rows={mode === 'panel' ? 3 : 2}
      placeholder={mode === 'tab' ? 'Your request… (Enter to send, Shift+Enter for a new line)' : 'Your request…'}
      aria-label="Your request"
      onkeydown={keydown}
      data-no-pin
    ></textarea>
    <button type="submit" class="primary" disabled={assistantUi.sending || !conversation.draft.trim()}>
      <Icon name="send" size={14} />
      {assistantUi.sending ? 'Sending…' : 'Send'}
    </button>
  </form>
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
  .panel .bar {
    padding-top: 0;
  }
  .title {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }
  .tab .title {
    font-size: 1rem;
  }
  .bar button {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }
  .settings {
    position: relative;
  }
  .set {
    padding: 0.6rem;
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
    font-size: 1.2rem;
  }
  .muted {
    color: var(--muted);
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: 0.6rem;
    margin-top: 0.8rem;
  }
  .panel .cards {
    grid-template-columns: 1fr;
  }
  .agent h3 {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    margin-bottom: 0.3rem;
  }
  .desc {
    font-size: 0.92em;
  }
  .sugg {
    display: grid;
    gap: 0.25rem;
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
    gap: 1rem;
  }
  .thread {
    display: grid;
    gap: 0.4rem;
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
    resize: none;
    min-height: 2.6rem;
  }
  .panel .compose {
    flex-direction: column;
    align-items: stretch;
  }
  .compose button {
    justify-content: center;
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }
</style>
