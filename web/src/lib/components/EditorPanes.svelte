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
  // by the caller (e.g. in the tab params) through `active`. The panes are also the editor's context: the context
  // bar lists them (with what the editor puts aside); while the bar is docked, they are there only.
  import type { Snippet } from 'svelte';
  import { contextBar, publishContext, withdrawContext } from '../shell/context.svelte';

  let {
    panes,
    active = $bindable(),
    label = 'Sections',
    changeId,
    toolbar,
    head,
    aside,
    children,
  }: {
    panes: Pane[];
    active: string;
    label?: string;
    /** the change these are the sections of (the change editor): the context bar binds them to it */
    changeId?: string;
    /** content on the right of the pane tabs (actions, working change…) */
    toolbar?: Snippet;
    /** what comes before the panes (the object's scope, its lifecycle…) */
    head?: Snippet;
    /** what the context bar holds under the sections (what waits for people…); above the content while it is not docked */
    aside?: Snippet;
    /** renders the pane with the given id */
    children: Snippet<[string]>;
  } = $props();

  const visible = $derived(panes.filter((p) => !p.hidden));
  const current = $derived(visible.find((p) => p.id === active)?.id ?? visible[0]?.id ?? '');
  const docked = $derived(contextBar.docked);

  // the context bar shows what this editor holds, while it is shown
  const owner = Symbol('panes');
  $effect(() => {
    publishContext({ owner, label, panes: visible, active: current, pick: (id) => (active = id), changeId, aside });
  });
  $effect(() => () => withdrawContext(owner));

  function onkey(e: KeyboardEvent, i: number) {
    const step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const to = visible[(i + step + visible.length) % visible.length];
    active = to.id;
    queueMicrotask(() => document.getElementById(`pane-tab-${to.id}`)?.focus());
  }
</script>

<div class="panes">
  {#if head}<div class="head">{@render head()}</div>{/if}
  {#if !docked || toolbar}
    <div class="bar" class:docked>
      {#if !docked}
        <div class="tabs" role="tablist" aria-label={label}>
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
              {#if p.tint}<span class="tint" style="background: {p.tint}" aria-hidden="true"></span>{/if}{p.label}{#if p.badge !== undefined && p.badge !== ''}<span class="badge">{p.badge}</span>{/if}
            </button>
          {/each}
        </div>
      {/if}
      <span class="grow"></span>
      {#if toolbar}<div class="tools">{@render toolbar()}</div>{/if}
    </div>
  {/if}
  {#if !docked && aside}<div class="aside-top">{@render aside()}</div>{/if}
  <div class="body" role="tabpanel" id="pane-{current}" aria-labelledby="pane-tab-{current}" aria-label={docked ? visible.find((p) => p.id === current)?.label : undefined}>
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
  .bar.docked {
    border-bottom: none;
  }
  .aside-top {
    margin-bottom: 0.8rem;
  }
  .body {
    min-width: 0;
  }
</style>
