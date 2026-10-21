<script lang="ts" module>
  export interface Pane {
    id: string;
    label: string;
    /** counter or short text shown next to the label */
    badge?: string | number;
    hidden?: boolean;
    /** a colour marking the pane as following a scope (a small dot before the label) */
    tint?: string;
    /** tooltip of the tab */
    title?: string;
  }
</script>

<script lang="ts">
  // The IDE's multi-pane editor: a main object opens in one editor tab and its
  // information is organized in sub-tabs (panes). The active pane is remembered
  // by the caller (e.g. in the tab params) through `active`. A wide editor shows the panes as a context column on
  // its right (the label its heading, the counts aligned), a narrow one as tabs above the content.
  import type { Snippet } from 'svelte';
  import { offerDock } from '../assistant/dock.svelte';

  let {
    panes,
    active = $bindable(),
    label = 'Sections',
    toolbar,
    head,
    aside,
    children,
  }: {
    panes: Pane[];
    active: string;
    label?: string;
    /** content on the right of the pane tabs (actions, working change…) */
    toolbar?: Snippet;
    /** what comes before the panes, the context column beside it (the object among its peers, its scope…) */
    head?: Snippet;
    /** what the context column holds under the panes (what waits for people…); above the content when narrow */
    aside?: Snippet;
    /** renders the pane with the given id */
    children: Snippet<[string]>;
  } = $props();

  const visible = $derived(panes.filter((p) => !p.hidden));
  /** the width from which the panes are a column: the editor's own width, not the window's */
  const RAIL_FROM = 900;
  let width = $state(0);
  const rail = $derived(width >= RAIL_FROM);
  const current = $derived(visible.find((p) => p.id === active)?.id ?? visible[0]?.id ?? '');

  function onkey(e: KeyboardEvent, i: number) {
    const [next, prev] = rail ? ['ArrowDown', 'ArrowUp'] : ['ArrowRight', 'ArrowLeft'];
    const step = e.key === next ? 1 : e.key === prev ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const to = visible[(i + step + visible.length) % visible.length];
    active = to.id;
    queueMicrotask(() => document.getElementById(`pane-tab-${to.id}`)?.focus());
  }
</script>

<div class="panes" class:rail bind:clientWidth={width}>
  {#if head}<div class="head">{@render head()}</div>{/if}
  <div class="bar">
    <div class="side">
    <div class="tabs" role="tablist" aria-label={label} aria-orientation={rail ? 'vertical' : 'horizontal'}>
      {#if rail}<div class="ui-section-head rail-head" aria-hidden="true">{label}</div>{/if}
      {#each visible as p, i (p.id)}
        <button
          type="button"
          role="tab"
          id="pane-tab-{p.id}"
          aria-selected={current === p.id}
          aria-controls="pane-{p.id}"
          tabindex={current === p.id ? 0 : -1}
          class:on={current === p.id}
          title={p.title}
          onclick={() => (active = p.id)}
          onkeydown={(e) => onkey(e, i)}
        >
          {#if p.tint}<span class="tint" style="background: {p.tint}" aria-hidden="true"></span>{/if}<span class="pl">{p.label}</span>{#if p.badge !== undefined && p.badge !== ''}<span class="badge">{p.badge}</span>{/if}
        </button>
      {/each}
    </div>
    {#if rail}
      {#if aside}<div class="aside">{@render aside()}</div>{/if}
      <!-- the assistant's conversation docks here -->
      <div class="dock" use:offerDock></div>
    {/if}
    </div>
    <span class="grow"></span>
    {#if toolbar}<div class="tools">{@render toolbar()}</div>{/if}
  </div>
  {#if !rail && aside}<div class="aside-top">{@render aside()}</div>{/if}
  <div class="body" role="tabpanel" id="pane-{current}" aria-labelledby="pane-tab-{current}">
    {@render children(current)}
  </div>
</div>

<style>
  .panes {
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .bar {
    display: flex;
    align-items: flex-end;
    gap: 0.5rem;
    border-bottom: 1px solid var(--border);
    margin-bottom: 0.8rem;
    flex-wrap: wrap;
  }
  .tabs {
    display: flex;
    gap: 0.1rem;
  }
  .tabs button {
    border: none;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    background: none;
    padding: 0.45rem 0.9rem;
    font-weight: 500;
    color: var(--muted);
    min-height: 0;
  }
  .tabs button:hover {
    color: var(--text);
  }
  .tabs button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .tint {
    display: inline-block;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    margin-right: 0.35rem;
    vertical-align: middle;
  }
  .badge {
    margin-left: 0.35rem;
    border-radius: 999px;
    background: var(--surface-2);
    padding: 0 0.4rem;
    font-size: 0.75rem;
    font-weight: 400;
  }
  .tools {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    padding-bottom: 0.3rem;
    flex-wrap: wrap;
  }
  .body {
    min-width: 0;
  }

  /* the context column: the content on the left, the panes on the right */
  .side {
    display: contents;
  }
  .aside-top {
    margin-bottom: 0.8rem;
  }
  .rail {
    display: grid;
    grid-template-columns: minmax(0, 1fr) var(--rightbar-w, 270px);
    grid-template-areas:
      'head nav'
      'tools nav'
      'body nav';
    grid-template-rows: auto auto 1fr;
    column-gap: var(--space-4);
    align-items: start;
  }
  .rail .bar {
    display: contents;
  }
  .rail .grow {
    display: none;
  }
  .rail .tools {
    grid-area: tools;
    justify-content: flex-end;
    padding-bottom: var(--space-2);
  }
  .rail .tools:empty {
    display: none;
  }
  .rail .head {
    grid-area: head;
    min-width: 0;
  }
  .rail .body {
    grid-area: body;
  }
  /* the column follows the scroll and stays within the editor's height (--area-h, set by the editor area) */
  .rail .side {
    grid-area: nav;
    grid-row: 1 / span 3;
    position: sticky;
    top: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    max-height: calc(var(--area-h, 100vh) - 1.8rem);
    min-height: 0;
  }
  .aside {
    flex: none;
    max-height: 40%;
    overflow: auto;
  }
  .dock {
    flex: 1 0 220px;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .dock:empty {
    display: none;
  }
  .rail .tabs {
    flex: 0 1 auto;
    min-height: 120px;
    overflow: auto;
    flex-direction: column;
    gap: 1px;
    padding: var(--space-2);
    background: var(--bg-panel);
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
  }
  .rail-head {
    padding: 2px 8px 6px;
  }
  .rail .tabs button {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    padding: 5px 8px;
    border: none;
    border-radius: var(--radius-s);
    text-align: left;
    font-size: 12.5px;
    font-weight: 400;
    color: var(--text-2);
  }
  .rail .tabs button:hover {
    background: var(--bg-raised);
    color: var(--text);
  }
  .rail .tabs button.on {
    background: var(--accent-subtle);
    color: var(--accent);
  }
  .rail .tint {
    margin-right: 0;
  }
  .rail .pl {
    flex: 1;
    min-width: 0;
  }
  .rail .badge {
    margin-left: 0;
    background: none;
    color: var(--text-3);
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
  }
  .rail .tabs button.on .badge {
    color: var(--accent);
  }
</style>
