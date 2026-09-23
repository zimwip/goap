<script lang="ts">
  // IDE shell: header, toolbar, activity bars, resizable panels,
  // editor area, and console. Content comes from the view
  // registry.
  import type { Component } from 'svelte';
  import Header from './Header.svelte';
  import Toolbar from './Toolbar.svelte';
  import ActivityBar from './ActivityBar.svelte';
  import SidePanel from './SidePanel.svelte';
  import EditorArea from './EditorArea.svelte';
  import BottomPanel from './BottomPanel.svelte';
  import Resizer from './Resizer.svelte';
  import Toasts from './Toasts.svelte';
  import StatusBar from './StatusBar.svelte';
  import { layout, LIMITS, toggleConsole } from './layout.svelte';
  import { activeTab, closeTab } from './tabs.svelte';
  import { runTabAction, focusRequests } from './workbench.svelte';

  let { welcome }: { welcome: Component } = $props();

  let width = $state(window.innerWidth);
  let height = $state(window.innerHeight);

  // Narrow screens: side panels switch to an overlay.
  const narrow = $derived(width < 1024);
  const sideMax = $derived(Math.max(LIMITS.side.min, Math.min(LIMITS.side.max, Math.floor(width * 0.4))));
  const bottomMax = $derived(Math.max(LIMITS.bottom.min, Math.min(LIMITS.bottom.max, height - 200)));

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
    } else if (mod && (k === 'p' || k === 'k')) {
      e.preventDefault();
      focusRequests.search += 1;
    }
  }
</script>

<svelte:window bind:innerWidth={width} bind:innerHeight={height} onkeydown={keydown} />

<div class="shell" class:narrow>
  <Header />
  <Toolbar />
  <div class="main">
    <ActivityBar side="left" />
    {#if layout.leftOpen}
      <div class="side left" style:width="{Math.min(layout.leftWidth, sideMax)}px">
        <SidePanel side="left" />
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
    {#if layout.rightOpen}
      {#if !narrow}
        <Resizer
          orientation="vertical"
          value={Math.min(layout.rightWidth, sideMax)}
          min={LIMITS.side.min}
          max={sideMax}
          invert
          label="Right side panel width"
          onresize={(v) => (layout.rightWidth = v)}
        />
      {/if}
      <div class="side right" style:width="{Math.min(layout.rightWidth, sideMax)}px">
        <SidePanel side="right" />
      </div>
    {/if}
    <ActivityBar side="right" />
  </div>
  <StatusBar />
</div>
<Toasts />

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
    left: 42px;
    border-right: 1px solid var(--border);
  }
  .narrow .side.right {
    right: 42px;
    border-left: 1px solid var(--border);
  }
</style>
