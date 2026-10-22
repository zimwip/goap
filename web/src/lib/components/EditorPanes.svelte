<script lang="ts" module>
  import type { IconName } from '../shell/Icon.svelte';

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
    /** the icon of the tab (else the one of its id) */
    icon?: IconName;
  }
</script>

<script lang="ts">
  // The IDE's multi-pane editor: a main object opens in one editor tab, split in three: its header (what comes before
  // the panes: the object's scope, its lifecycle…), the strip of its sections (panes) as tabs with the editor's tools on
  // their right, and the detail of the section picked. The active pane is remembered by the caller (e.g. in the tab
  // params) through `active`.
  import type { Snippet } from 'svelte';
  import Icon from '../shell/Icon.svelte';
  import { paneIcon } from './paneIcons';

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
    /** content on the right of the tabs (actions, working change…) */
    toolbar?: Snippet;
    /** the header of the editor, above the tabs (the object's scope, its lifecycle…) */
    head?: Snippet;
    /** what comes above every pane, under the tabs (what waits for people…) */
    aside?: Snippet;
    /** renders the pane with the given id */
    children: Snippet<[string]>;
  } = $props();

  const visible = $derived(panes.filter((p) => !p.hidden));
  const current = $derived(visible.find((p) => p.id === active)?.id ?? visible[0]?.id ?? '');

  function onkey(e: KeyboardEvent, i: number) {
    const step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    const to = e.key === 'Home' ? 0 : e.key === 'End' ? visible.length - 1 : (i + step + visible.length) % visible.length;
    if (!step && e.key !== 'Home' && e.key !== 'End') return;
    e.preventDefault();
    const next = visible[to];
    active = next.id;
    queueMicrotask(() => document.getElementById(`pane-tab-${next.id}`)?.focus());
  }
</script>

<div class="panes">
  {#if head}<div class="head">{@render head()}</div>{/if}
  <div class="bar">
    <div class="tabs" role="tablist" aria-label={label}>
      {#each visible as p, i (p.id)}
        {@const on = current === p.id}
        <button
          type="button"
          role="tab"
          id="pane-tab-{p.id}"
          aria-selected={on}
          aria-controls="pane-{p.id}"
          tabindex={on ? 0 : -1}
          class:on
          title={p.title ?? (p.badge !== undefined && p.badge !== '' ? `${p.label} · ${p.badge}` : p.label)}
          onclick={() => (active = p.id)}
          onkeydown={(e) => onkey(e, i)}
        >
          <Icon name={paneIcon(p)} size={13} />
          {#if p.tint}<span class="tint" style="background: {p.tint}" aria-hidden="true"></span>{/if}
          <span class="label">{p.label}</span>
          {#if p.badge !== undefined && p.badge !== ''}<span class="badge">{p.badge}</span>{/if}
        </button>
      {/each}
    </div>
    {#if toolbar}<div class="tools">{@render toolbar()}</div>{/if}
  </div>
  {#if aside}<div class="aside-top">{@render aside()}</div>{/if}
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
  .head {
    margin-bottom: 0.6rem;
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
    flex: 1;
    min-width: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.1rem;
  }
  .tabs button {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    border: none;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    background: none;
    padding: 0.45rem 0.75rem;
    margin-bottom: -1px;
    font-weight: 500;
    color: var(--text-3, var(--muted));
    min-height: 0;
    white-space: nowrap;
  }
  .tabs button:hover:not(:disabled) {
    background: var(--hover);
    color: var(--text);
  }
  .tabs button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .tabs button.on :global(svg) {
    color: var(--accent);
  }
  .tabs button:focus-visible {
    outline-offset: -2px;
  }
  .tint {
    display: inline-block;
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }
  .badge {
    border-radius: 999px;
    background: var(--surface-2);
    padding: 0 0.4rem;
    font-family: var(--font-mono);
    font-size: var(--fs-xs, 0.75rem);
    font-weight: 400;
    color: var(--text-3, var(--muted));
  }
  .tabs button.on .badge {
    color: var(--accent);
  }
  .tools {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    padding-bottom: 0.3rem;
    flex-wrap: wrap;
  }
  .aside-top {
    margin-bottom: 0.8rem;
  }
  .body {
    min-width: 0;
  }
</style>
