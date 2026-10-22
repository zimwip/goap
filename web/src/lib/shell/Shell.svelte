<script lang="ts">
  // IDE shell: header (with the editor's actions), navigation sidebar, resizable panels,
  // editor area, and console. Content comes from the view
  // registry.
  import { onMount, type Component } from 'svelte';
  import Header from './Header.svelte';
  import ActivityBar from './ActivityBar.svelte';
  import NavSidebar from './NavSidebar.svelte';
  import ContextBar from './ContextBar.svelte';
  import Icon from './Icon.svelte';
  import { panelView } from './registry';
  import { contextBar } from './context.svelte';
  import EditorArea from './EditorArea.svelte';
  import BottomPanel from './BottomPanel.svelte';
  import Resizer from './Resizer.svelte';
  import Toasts from './Toasts.svelte';
  import ContextMenu from './ContextMenu.svelte';
  import ObjectDialog from './ObjectDialog.svelte';
  import FieldDialog from './FieldDialog.svelte';
  import PickerModal from './PickerModal.svelte';
  import MergeDialog from './MergeDialog.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import ConditionExplainDialog from './ConditionExplainDialog.svelte';
  import ModelExchangeDialog from './ModelExchangeDialog.svelte';
  import SettingsDialog from './SettingsDialog.svelte';
  import UsageDialog from './UsageDialog.svelte';
  import PlatformStatusModal from './PlatformStatusModal.svelte';
  import SearchOverlay from './SearchOverlay.svelte';
  import { openSearch } from './searchOverlay.svelte.ts';
  import HelperBubble from '../helper/HelperBubble.svelte';
  import { canOpenHelper, closeHelper, openHelper } from '../helper/helper.svelte';
  import Launcher from '../assistant/Launcher.svelte';
  import { assistantEnabled } from '../assistant/enabled';
  import { assistant, closePanel, openPanel } from '../stores/assistant.svelte';
  import StatusBar from './StatusBar.svelte';
  import { layout, LIMITS, RAIL, nav, toggleConsole } from './layout.svelte';
  import { activeTab, closeTab, startRouting } from './tabs.svelte';
  import { runTabAction, focusRequests } from './workbench.svelte';
  import { session } from '../stores/session.svelte';
  import { isPending } from '../stores/pending.svelte';
  import { loadSettingsState } from '../stores/settings.svelte';
  import { followTasks } from '../stores/tasks.svelte';

  let { welcome }: { welcome: Component } = $props();

  // the address says which tab is shown: follow it from the moment the views are registered
  onMount(startRouting);

  // the person's tasks are counted on the Tasks tool whatever the tool shown (ADR 0158)
  $effect(() => (session.loaded ? followTasks() : undefined));

  // the preferences are graph data of the signed-in user: read them once the identity is known (and again when it changes)
  $effect(() => {
    void session.principal?.subject;
    if (session.loaded) void loadSettingsState();
  });

  // unsaved settings are lost on leaving the page: the browser asks first
  function beforeUnload(e: BeforeUnloadEvent) {
    if (isPending()) e.preventDefault();
  }

  let width = $state(window.innerWidth);
  let height = $state(window.innerHeight);

  // Narrow screens: side panels switch to an overlay.
  const narrow = $derived(width < 1024);
  const sideMax = $derived(Math.max(LIMITS.side.min, Math.min(LIMITS.side.max, Math.floor(width * 0.4))));
  const bottomMax = $derived(Math.max(LIMITS.bottom.min, Math.min(LIMITS.bottom.max, height - 200)));

  // Both side bars work alike: a rail of icons with its pin at the top. Pinned on a wide screen, the bar is docked beside
  // the editor; else a click on an icon (or the focus) opens it over the editor, it follows the icon hovered while open,
  // and it closes once the pointer and the focus left it.
  const HIDE_DELAY = 350;

  /** Whether the focus is on something the person is working with in the bar (a filter typed, a menu open). */
  function busy(bar: HTMLElement | undefined): boolean {
    const el = document.activeElement;
    return !!bar?.contains(el) && el !== document.body && !!el?.matches('input, textarea, select, [aria-expanded="true"]');
  }

  // The context bar (right): open, its rail slides out over the editor with the labels.
  const docked = $derived(layout.rightOpen && !narrow);
  $effect(() => {
    contextBar.docked = docked;
  });
  const ctxMax = $derived(Math.min(LIMITS.context.max, sideMax));
  const ctxWidth = $derived(Math.max(LIMITS.context.min, Math.min(layout.contextWidth, ctxMax)));
  let rail = $state<HTMLElement>();
  let hideTimer: ReturnType<typeof setTimeout> | undefined;
  function ctxLeave() {
    clearTimeout(hideTimer);
    hideTimer = setTimeout(() => {
      if (!busy(rail)) contextBar.revealed = false;
    }, HIDE_DELAY);
  }
  function ctxFocusOut(e: FocusEvent) {
    if (!rail?.contains(e.relatedTarget as Node | null) && !rail?.matches(':hover')) contextBar.revealed = false;
  }
  function ctxKey(e: KeyboardEvent) {
    if (e.key === 'Escape' && contextBar.revealed) {
      contextBar.revealed = false;
      e.stopPropagation();
    }
  }

  // The navigation (left): open, the tool shown appears beside its rail.
  const leftDocked = $derived(layout.leftOpen && !narrow);
  let lrail = $state<HTMLElement>();
  let navTimer: ReturnType<typeof setTimeout> | undefined;
  function navLeave() {
    clearTimeout(navTimer);
    navTimer = setTimeout(() => {
      if (!busy(lrail)) nav.revealed = false;
    }, HIDE_DELAY);
  }
  function navFocusOut(e: FocusEvent) {
    if (!lrail?.contains(e.relatedTarget as Node | null) && !lrail?.matches(':hover')) nav.revealed = false;
  }
  function navKey(e: KeyboardEvent) {
    if (e.key === 'Escape' && nav.revealed) {
      nav.revealed = false;
      e.stopPropagation();
    }
  }

  // the side tool (Test, Properties, DSL Help), opened from the status bar
  const tool = $derived(layout.drawer ? panelView(layout.right) : undefined);
  function toolKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      layout.drawer = false;
      e.stopPropagation();
    }
  }

  function keydown(e: KeyboardEvent) {
    const mod = e.ctrlKey || e.metaKey;
    const k = e.key.toLowerCase();
    if (mod && !e.shiftKey && !e.altKey && k === 's') {
      e.preventDefault();
      const t = activeTab();
      if (t) runTabAction(t.id, 'save');
    } else if ((mod && !e.shiftKey && k === 'w') || (e.altKey && !mod && k === 'w')) {
      e.preventDefault();
      const t = activeTab();
      if (t) closeTab(t.id);
    } else if (mod && !e.shiftKey && k === 'j') {
      e.preventDefault();
      toggleConsole();
    } else if (mod && !e.shiftKey && k === 'b') {
      e.preventDefault();
      layout.leftOpen = !layout.leftOpen;
    } else if (mod && e.shiftKey && k === 'f') {
      e.preventDefault();
      openSearch();
    } else if (mod && !e.shiftKey && !e.altKey && k === '.') {
      e.preventDefault();
      if (canOpenHelper()) void openHelper();
    } else if (mod && e.shiftKey && !e.altKey && k === 'a') {
      e.preventDefault();
      if (assistant.panelOpen) closePanel();
      else if (assistantEnabled()) (closeHelper(), openPanel());
    } else if (mod && (k === 'p' || k === 'k')) {
      e.preventDefault();
      focusRequests.search += 1;
    }
  }
</script>

<svelte:window bind:innerWidth={width} bind:innerHeight={height} onkeydown={keydown} onbeforeunload={beforeUnload} />

<div class="shell" class:narrow>
  <Header />
  <div class="main">
    {#if !leftDocked}
      <!-- the navigation's rail: a click on a tool (or the focus) shows the navigation over the editor; leaving it hides it -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class="lrail"
        bind:this={lrail}
        onmouseenter={() => clearTimeout(navTimer)}
        onmouseleave={navLeave}
        onfocusin={(e) => {
          if (!(e.target as HTMLElement).matches('.pin')) nav.revealed = true;
        }}
        onfocusout={navFocusOut}
        onkeydown={navKey}
      >
        <ActivityBar side="left" />
        {#if nav.revealed}
          <div class="side left over" style:width="{Math.min(layout.leftWidth, sideMax)}px">
            <NavSidebar />
          </div>
        {/if}
      </div>
    {:else}
      <!-- pinned: the rail stays, its pin at the top, the navigation docked beside it -->
      <div class="lrail">
        <ActivityBar side="left" />
        <div class="side left" style:width="{Math.min(layout.leftWidth, sideMax)}px">
          <NavSidebar />
        </div>
      </div>
      {#if !narrow}
        <Resizer
          orientation="vertical"
          value={Math.min(layout.leftWidth, sideMax)}
          min={LIMITS.side.min}
          max={sideMax}
          label="Navigation panel width"
          onresize={(v) => (layout.leftWidth = v)}
        />
      {/if}
    {/if}
    <div class="center">
      <div class="editor">
        <EditorArea {welcome} />
      </div>
      {#if layout.bottomOpen}
        <Resizer
          orientation="horizontal"
          value={Math.min(layout.bottomHeight, bottomMax)}
          min={LIMITS.bottom.min}
          max={bottomMax}
          invert
          label="Console height"
          onresize={(v) => (layout.bottomHeight = v)}
        />
        <div class="bottom" style:height="{Math.min(layout.bottomHeight, bottomMax)}px">
          <BottomPanel />
        </div>
      {/if}
    </div>
    {#if tool}
      {@const C = tool.component}
      {#if !narrow}
        <Resizer
          orientation="vertical"
          value={Math.min(layout.rightWidth, sideMax)}
          min={LIMITS.side.min}
          max={sideMax}
          invert
          label="Side tool width"
          onresize={(v) => (layout.rightWidth = v)}
        />
      {/if}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="side right tool" style:width="{Math.min(layout.rightWidth, sideMax)}px" onkeydown={toolKey}>
        <div class="tool-head">
          <Icon name={tool.icon} size={13} />
          <span class="tool-title">{tool.title}</span>
          <button type="button" class="ghost small" title="Close" aria-label="Close {tool.title}" onclick={() => (layout.drawer = false)}><Icon name="x" size={13} /></button>
        </div>
        <div class="tool-body"><C /></div>
      </div>
    {/if}
    {#if docked}
      <Resizer
        orientation="vertical"
        value={ctxWidth}
        min={LIMITS.context.min}
        max={ctxMax}
        invert
        label="Context bar width"
        onresize={(v) => (layout.contextWidth = v)}
      />
      <div class="ctx-docked" style:width="{ctxWidth}px">
        <ContextBar open />
      </div>
    {:else}
      <!-- the context bar's rail: a click on a section (or the focus) slides the bar open over the editor; leaving it hides it -->
      <div class="rail-slot" style:width="{RAIL}px">
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="rail"
          class:open={contextBar.revealed}
          bind:this={rail}
          style:width="{contextBar.revealed ? ctxWidth : RAIL}px"
          onmouseenter={() => clearTimeout(hideTimer)}
          onmouseleave={ctxLeave}
          onfocusin={(e) => {
            if (!(e.target as HTMLElement).matches('.pin')) contextBar.revealed = true;
          }}
          onfocusout={ctxFocusOut}
          onkeydown={ctxKey}
        >
          <ContextBar open={contextBar.revealed} />
        </div>
      </div>
    {/if}
  </div>
  <StatusBar />
</div>
<Toasts />
<ContextMenu />
<ObjectDialog />
<FieldDialog />
<PickerModal />
<MergeDialog />
<ConfirmDialog />
<ConditionExplainDialog />
<ModelExchangeDialog />
<SettingsDialog />
<UsageDialog />
<PlatformStatusModal />
<SearchOverlay />
<HelperBubble />
<Launcher />

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100vh;
    height: 100dvh;
    min-width: 320px;
  }
  .main {
    flex: 1;
    display: flex;
    min-height: 0;
    position: relative;
  }
  .side {
    flex: none;
    min-width: 0;
    height: 100%;
  }
  .center {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .editor {
    flex: 1;
    min-height: 0;
  }
  .bottom {
    flex: none;
    min-height: 0;
  }
  .lrail {
    flex: none;
    position: relative;
    display: flex;
    height: 100%;
  }
  /* the navigation shown over the editor, by its rail */
  .side.left.over {
    position: absolute;
    top: 0;
    bottom: 0;
    left: 100%;
    z-index: 25;
    max-width: calc(100vw - 120px);
    border-right: 1px solid var(--border);
    box-shadow: var(--shadow);
  }
  .ctx-docked {
    flex: none;
    height: 100%;
    min-width: 0;
  }
  .rail-slot {
    flex: none;
    position: relative;
    height: 100%;
  }
  /* the rail slides open over the editor, from the right edge */
  .rail {
    position: absolute;
    top: 0;
    bottom: 0;
    right: 0;
    z-index: 20;
    transition: width 0.18s ease;
  }
  .rail.open {
    box-shadow: var(--shadow);
  }
  @media (prefers-reduced-motion: reduce) {
    .rail {
      transition: none;
    }
  }
  .tool {
    display: flex;
    flex-direction: column;
    background: var(--bg-panel);
    border-left: 1px solid var(--border);
  }
  .tool-head {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 32px;
    flex: none;
    padding: 0 4px 0 10px;
    border-bottom: 1px solid var(--border);
    color: var(--text-3);
  }
  .tool-title {
    flex: 1;
    font-size: var(--fs-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
  .tool-body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    display: flex;
    flex-direction: column;
  }
  /* Narrow: side panels overlaid on the editor. */
  .narrow .side {
    position: absolute;
    top: 0;
    bottom: 0;
    z-index: 20;
    box-shadow: var(--shadow-pop);
    max-width: calc(100% - 84px);
  }
  .narrow .side.left {
    left: 0;
    border-right: 1px solid var(--border);
  }
  .narrow .side.right {
    right: 44px;
    border-left: 1px solid var(--border);
  }
</style>
