<script lang="ts">
  // The floating assistant (ADR 0087): a round bubble on every screen that opens the conversation as a compact panel.
  // Not rendered when the assistant alias has no model. It keeps clear of the right activity bar and tool panel, the
  // console and the status bar, and gives way to the field helper (ADR 0086): they are never open together.
  import { tick } from 'svelte';
  import Icon from '../shell/Icon.svelte';
  import Assistant from '../views/assistant/Assistant.svelte';
  import { layout } from '../shell/layout.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { helper } from '../helper/helper.svelte';
  import { assistant, closePanel, openPanel } from '../stores/assistant.svelte';
  import { assistantEnabled } from './enabled';

  const enabled = $derived(assistantEnabled());
  let button = $state<HTMLButtonElement>();
  let returnFocus: Element | null = null;

  // keep clear of what is docked at the right and at the bottom
  const ACTIVITY_BAR = 42;
  const STATUS_BAR = 22;
  const right = $derived(ACTIVITY_BAR + (layout.rightOpen ? layout.rightWidth : 0) + 12);
  const bottom = $derived(STATUS_BAR + (layout.bottomOpen ? layout.bottomHeight : 0) + 12);

  // the helper and the panel never share the screen
  $effect(() => {
    if (helper.open) closePanel();
  });
  $effect(() => {
    if (!enabled) closePanel();
  });

  // focus goes into the panel when it opens and back where it was when it closes
  $effect(() => {
    if (!assistant.panelOpen) return;
    returnFocus = document.activeElement;
    return () => {
      const el = returnFocus;
      returnFocus = null;
      if (el instanceof HTMLElement && el.isConnected) el.focus();
      else void tick().then(() => button?.focus());
    };
  });

  function toggle() {
    if (assistant.panelOpen) closePanel();
    else openPanel();
  }

  function expand() {
    closePanel();
    openTab({ kind: 'assistant', params: {} }, { pin: true });
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      closePanel();
    }
  }
</script>

{#if enabled && !helper.open}
  {#if assistant.panelOpen}
    <div
      class="panel"
      role="dialog"
      aria-modal="false"
      aria-label="Assistant"
      tabindex="-1"
      style:right="{right}px"
      style:bottom="{bottom + 52}px"
      onkeydown={keydown}
    >
      <Assistant mode="panel" onclose={closePanel} onexpand={expand} />
    </div>
  {/if}
  <button
    type="button"
    class="launcher"
    class:active={assistant.panelOpen}
    bind:this={button}
    style:right="{right}px"
    style:bottom="{bottom}px"
    aria-label="Ask the assistant"
    aria-expanded={assistant.panelOpen}
    title="Ask the assistant (Ctrl+Shift+A)"
    onclick={toggle}
  >
    <Icon name="chat" size={20} />
  </button>
{/if}

<style>
  .launcher {
    position: fixed;
    z-index: 75;
    width: 44px;
    height: 44px;
    padding: 0;
    border-radius: 50%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--accent);
    color: var(--accent-text);
    border: 1px solid var(--border);
    box-shadow: var(--shadow-pop);
  }
  .launcher:hover,
  .launcher.active {
    filter: brightness(1.1);
  }
  .panel {
    position: fixed;
    z-index: 75;
    width: min(380px, calc(100vw - 24px));
    height: min(520px, calc(100dvh - 140px));
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
  }
</style>
