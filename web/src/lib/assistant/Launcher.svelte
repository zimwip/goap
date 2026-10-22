<script lang="ts">
  // Not mounted any more (the assistant lives in the Assistant bar, `shell/AssistantBar.svelte`): the design of this
  // floating bubble is kept for another use. The floating assistant (ADR 0087): a round bubble on every screen that opens the conversation as a compact panel
  // by it. Not rendered when the assistant alias has no model. It sits in the bottom-right corner, above the status
  // bar, and the person moves it where they like by dragging it (Alt+arrows from the keyboard); the browser keeps the
  // place. It gives way to the field helper (ADR 0086): they are never open together.
  import { tick } from 'svelte';
  import Icon from '../shell/Icon.svelte';
  import Assistant from '../views/assistant/Assistant.svelte';
  import { load, save } from '../shell/storage';
  import { openTab } from '../shell/tabs.svelte';
  import { helper } from '../helper/helper.svelte';
  import { assistant, closePanel, openPanel } from '../stores/assistant.svelte';
  import { assistantEnabled } from './enabled';
  import { startCapture } from '../assist/capture';
  import { entityOfElement } from '../assist/registry.svelte';

  // what is selected or focused on the page is remembered, for the assistant to use (ADR 0092)
  $effect(() => startCapture((el) => !!entityOfElement(el)));

  const enabled = $derived(assistantEnabled());
  let button = $state<HTMLButtonElement>();
  let returnFocus: Element | null = null;

  // where the bubble is: its distance to the right and bottom edges of the window, kept by the browser
  const KEY = 'goap.ide.assistantBubble';
  const SIZE = 44;
  const MARGIN = 8;
  const STATUS_BAR = 24;
  const HOME = { right: 12, bottom: STATUS_BAR + 10 };
  let place = $state(load(KEY, HOME));
  let vw = $state(window.innerWidth);
  let vh = $state(window.innerHeight);
  const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));
  // the bubble stays on screen whatever the window's size
  const right = $derived(clamp(place.right, MARGIN, vw - SIZE - MARGIN));
  const bottom = $derived(clamp(place.bottom, MARGIN, vh - SIZE - MARGIN));

  // the panel opens by the bubble: above it when there is room, else below, kept on screen; the person resizes it
  // by its top-left corner (the browser keeps the size)
  const SIZE_KEY = 'goap.ide.assistantPanel';
  const MIN = { w: 300, h: 280 };
  let size = $state(load(SIZE_KEY, { w: 380, h: 520 }));
  const panelW = $derived(clamp(size.w, MIN.w, vw - 2 * MARGIN));
  const panelH = $derived(clamp(size.h, MIN.h, vh - STATUS_BAR - 2 * MARGIN));
  const panelRight = $derived(clamp(right, MARGIN, vw - panelW - MARGIN));
  const below = $derived(bottom + SIZE + MARGIN + panelH > vh - MARGIN);
  const panelBottom = $derived(clamp(below ? bottom - panelH - MARGIN : bottom + SIZE + MARGIN, MARGIN, vh - panelH - MARGIN));

  // dragging: a press that moves more than a few pixels moves the bubble instead of opening it
  let drag: { x: number; y: number; right: number; bottom: number; moved: boolean } | undefined;
  let dragged = false;
  function down(e: PointerEvent) {
    if (e.button !== 0) return;
    drag = { x: e.clientX, y: e.clientY, right, bottom, moved: false };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }
  function move(e: PointerEvent) {
    if (!drag) return;
    const dx = e.clientX - drag.x;
    const dy = e.clientY - drag.y;
    if (!drag.moved && Math.hypot(dx, dy) < 4) return;
    drag.moved = true;
    place = { right: clamp(drag.right - dx, MARGIN, vw - SIZE - MARGIN), bottom: clamp(drag.bottom - dy, MARGIN, vh - SIZE - MARGIN) };
  }
  function up() {
    if (!drag) return;
    dragged = drag.moved;
    if (drag.moved) save(KEY, place);
    drag = undefined;
  }
  function nudge(e: KeyboardEvent) {
    if (!e.altKey) return;
    const step = e.shiftKey ? 80 : 20;
    const d = { ArrowLeft: [step, 0], ArrowRight: [-step, 0], ArrowUp: [0, step], ArrowDown: [0, -step] }[e.key];
    if (!d) return;
    e.preventDefault();
    place = { right: clamp(right + d[0], MARGIN, vw - SIZE - MARGIN), bottom: clamp(bottom + d[1], MARGIN, vh - SIZE - MARGIN) };
    save(KEY, place);
  }
  let resizing: { x: number; y: number; w: number; h: number } | undefined;
  function resizeDown(e: PointerEvent) {
    if (e.button !== 0) return;
    e.preventDefault();
    resizing = { x: e.clientX, y: e.clientY, w: panelW, h: panelH };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }
  function resizeMove(e: PointerEvent) {
    if (!resizing) return;
    // the grip is at the corner away from the bubble: the top one above it, the bottom one below it
    const dy = e.clientY - resizing.y;
    size = { w: Math.round(resizing.w - (e.clientX - resizing.x)), h: Math.round(resizing.h + (below ? dy : -dy)) };
  }
  function resizeUp() {
    if (!resizing) return;
    resizing = undefined;
    size = { w: panelW, h: panelH };
    save(SIZE_KEY, size);
  }
  function resizeKey(e: KeyboardEvent) {
    const step = e.shiftKey ? 80 : 20;
    const v = below ? -step : step;
    const d = { ArrowLeft: [step, 0], ArrowRight: [-step, 0], ArrowUp: [0, v], ArrowDown: [0, -v] }[e.key];
    if (!d) return;
    e.preventDefault();
    e.stopPropagation();
    size = { w: clamp(panelW + d[0], MIN.w, vw - 2 * MARGIN), h: clamp(panelH + d[1], MIN.h, vh - STATUS_BAR - 2 * MARGIN) };
    save(SIZE_KEY, size);
  }

  function home() {
    place = HOME;
    save(KEY, place);
  }

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
    // the end of a drag is no click
    if (dragged) {
      dragged = false;
      return;
    }
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

<svelte:window bind:innerWidth={vw} bind:innerHeight={vh} />

{#if enabled && !helper.open}
  {#if assistant.panelOpen}
    <div
      class="panel"
      role="dialog"
      aria-modal="false"
      aria-label="Assistant"
      tabindex="-1"
      style:right="{panelRight}px"
      style:bottom="{panelBottom}px"
      style:width="{panelW}px"
      style:height="{panelH}px"
      onkeydown={keydown}
    >
      <Assistant mode="panel" onclose={closePanel} onexpand={expand} />
      <!-- the grip that resizes the panel (arrows from the keyboard) -->
      <button
        type="button"
        class="grip"
        class:below
        title="Drag to resize (arrows from the keyboard)"
        aria-label="Resize the assistant"
        onpointerdown={resizeDown}
        onpointermove={resizeMove}
        onpointerup={resizeUp}
        onpointercancel={resizeUp}
        onkeydown={resizeKey}
      ></button>
    </div>
  {/if}
  <button
    type="button"
    data-assistant
    class="launcher"
    class:active={assistant.panelOpen}
    bind:this={button}
    style:right="{right}px"
    style:bottom="{bottom}px"
    aria-label="Ask the assistant"
    aria-expanded={assistant.panelOpen}
    aria-keyshortcuts="Control+Shift+A"
    title="Ask the assistant (Ctrl+Shift+A). Drag it to move it (Alt+arrows); double-click to put it back in the corner"
    onclick={toggle}
    ondblclick={home}
    onpointerdown={down}
    onpointermove={move}
    onpointerup={up}
    onpointercancel={up}
    onkeydown={nudge}
  >
    <Icon name="chat" size={20} />
  </button>
{/if}

<style>
  .grip {
    position: absolute;
    top: 0;
    left: 0;
    width: 16px;
    height: 16px;
    min-height: 0;
    padding: 0;
    border: none;
    border-radius: var(--radius) 0 0 0;
    background: linear-gradient(135deg, var(--border-strong) 0 2px, transparent 2px 5px, var(--border-strong) 5px 6px, transparent 6px);
    cursor: nwse-resize;
    touch-action: none;
    z-index: 2;
    opacity: 0.7;
  }
  .grip.below {
    top: auto;
    bottom: 0;
    border-radius: 0 0 0 var(--radius);
    transform: scaleY(-1);
  }
  .grip:hover:not(:disabled),
  .grip:focus-visible {
    opacity: 1;
    background: linear-gradient(135deg, var(--accent) 0 2px, transparent 2px 5px, var(--accent) 5px 6px, transparent 6px);
  }
  .launcher {
    position: fixed;
    touch-action: none;
    cursor: grab;
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
  .launcher:active {
    cursor: grabbing;
  }
  .launcher:hover,
  .launcher.active {
    filter: brightness(1.1);
  }
  .panel {
    position: fixed;
    z-index: 75;
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
