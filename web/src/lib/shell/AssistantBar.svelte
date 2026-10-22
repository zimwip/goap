<script lang="ts">
  // The Assistant bar (right): the assistant and the discussions going on (ADR 0087, 0088). Closed, it is a rail: its
  // pin, the assistant, then the latest discussions; a click opens it over the editor, pinned it is docked beside the
  // editor, and either way it is resized by its left edge. The sections of an editor are its own tabs, never here.
  import Icon from './Icon.svelte';
  import Assistant from '../views/assistant/Assistant.svelte';
  import { layout } from './layout.svelte';
  import { openTab } from './tabs.svelte';
  import { assistant, closePanel, isPending, loadConversations, newConversation, openConversation, openPanel } from '../stores/assistant.svelte';
  import { assistantEnabled } from '../assistant/enabled';
  import { session } from '../stores/session.svelte';

  let { open = false, docked = false }: { open?: boolean; docked?: boolean } = $props();

  const enabled = $derived(assistantEnabled());
  const pending = $derived(isPending());
  /** the discussions the rail shows, newest first */
  const RAIL_DISCUSSIONS = 6;
  const recent = $derived(assistant.conversations.slice(0, RAIL_DISCUSSIONS));

  // the rail lists the discussions before any view of a conversation is shown
  $effect(() => {
    if (enabled && session.loaded && !assistant.loaded && !assistant.loading) void loadConversations();
  });

  function pin() {
    layout.rightOpen = !layout.rightOpen;
    if (layout.rightOpen) closePanel();
  }

  function show(id?: string) {
    if (id === '') newConversation();
    else if (id) void openConversation(id);
    openPanel();
  }

  function expand() {
    closePanel();
    openTab({ kind: 'assistant', params: {} }, { pin: true });
  }

  const initial = (t: string) => (t.trim()[0] ?? '?').toUpperCase();
</script>

<aside class="abar" class:open aria-label="Assistant">
  {#if open}
    <Assistant mode="side" onclose={docked ? undefined : closePanel} onexpand={expand}>
      {#snippet lead()}
        <button
          type="button"
          class="pin"
          class:on={layout.rightOpen}
          aria-pressed={layout.rightOpen}
          title={layout.rightOpen ? 'Unpin: the bar closes to its icons' : 'Pin the bar open beside the editor'}
          aria-label="Pin the Assistant bar"
          onclick={pin}><Icon name="pin" size={14} /></button
        >
      {/snippet}
    </Assistant>
  {:else}
    <div class="rail">
      <button
        type="button"
        class="pin"
        aria-pressed="false"
        title="Pin the bar open beside the editor"
        aria-label="Pin the Assistant bar"
        onclick={pin}><Icon name="pin" size={14} /></button
      >
      <button
        type="button"
        class="ic main"
        data-assistant
        title="Ask the assistant (Ctrl+Shift+A)"
        aria-label="Ask the assistant"
        aria-keyshortcuts="Control+Shift+A"
        onclick={() => show()}
      >
        <Icon name="chat" size={17} />
        {#if pending}<span class="live" aria-hidden="true"></span>{/if}
      </button>
      {#if enabled}
        <button type="button" class="ic" title="New conversation" aria-label="New conversation" onclick={() => show('')}><Icon name="plus" size={14} /></button>
        {#if recent.length}<span class="sep" aria-hidden="true"></span>{/if}
        {#each recent as c (c.id)}
          {@const on = c.id === assistant.currentId}
          <button type="button" class="ic disc" class:on title={c.title || 'Untitled'} aria-label="Discussion: {c.title || 'Untitled'}" onclick={() => show(c.id)}>
            <span class="letter">{initial(c.title || 'Untitled')}</span>
          </button>
        {/each}
      {/if}
    </div>
  {/if}
</aside>

<style>
  .abar {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    overflow: hidden;
    background: var(--bg-panel);
    border-left: 1px solid var(--border);
  }
  .rail {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    overflow-y: auto;
    overflow-x: hidden;
    scrollbar-width: none;
  }
  .pin,
  .ic {
    border: none;
    border-radius: 0;
    background: none;
    color: var(--text-3);
    padding: 0;
    min-height: 0;
  }
  .pin {
    width: 30px;
    height: 30px;
    flex: none;
    display: grid;
    place-items: center;
  }
  .rail .pin {
    width: 43px;
    height: 36px;
  }
  .pin:hover:not(:disabled) {
    background: none;
    color: var(--text);
  }
  /* the pin: slanted while the bar closes to its rail, upright and accented while pinned */
  .pin :global(svg) {
    transform: rotate(45deg);
    transition: transform var(--transition);
  }
  .pin.on,
  .pin.on:hover:not(:disabled) {
    color: var(--accent);
  }
  .pin.on :global(svg) {
    transform: none;
  }
  .ic {
    position: relative;
    width: 34px;
    height: 34px;
    flex: none;
    display: grid;
    place-items: center;
    border-radius: var(--radius);
  }
  .ic:hover:not(:disabled) {
    background: var(--hover);
    color: var(--text);
  }
  .ic.main {
    color: var(--accent);
  }
  .live {
    position: absolute;
    top: 6px;
    right: 6px;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--accent);
  }
  .sep {
    width: 20px;
    height: 1px;
    margin: 4px 0;
    background: var(--border);
  }
  .letter {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    border: 1px solid var(--border-strong, var(--border));
    font-size: 11px;
    font-weight: 600;
  }
  .disc.on .letter {
    border-color: var(--accent);
    color: var(--accent);
  }
  .pin:focus-visible,
  .ic:focus-visible {
    outline-offset: -2px;
  }
</style>
