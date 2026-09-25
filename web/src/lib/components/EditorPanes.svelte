<script lang="ts" module>
  export interface Pane {
    id: string;
    label: string;
    /** counter or short text shown next to the label */
    badge?: string | number;
    hidden?: boolean;
  }
</script>

<script lang="ts">
  // The IDE's multi-pane editor: a main object opens in one editor tab and its
  // information is organized in sub-tabs (panes). The active pane is remembered
  // by the caller (e.g. in the tab params) through `active`.
  import type { Snippet } from 'svelte';

  let {
    panes,
    active = $bindable(),
    label = 'Sections',
    toolbar,
    children,
  }: {
    panes: Pane[];
    active: string;
    label?: string;
    /** content on the right of the pane tabs (actions, working change…) */
    toolbar?: Snippet;
    /** renders the pane with the given id */
    children: Snippet<[string]>;
  } = $props();

  const visible = $derived(panes.filter((p) => !p.hidden));
  const current = $derived(visible.find((p) => p.id === active)?.id ?? visible[0]?.id ?? '');

  function onkey(e: KeyboardEvent, i: number) {
    const step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const next = visible[(i + step + visible.length) % visible.length];
    active = next.id;
    queueMicrotask(() => document.getElementById(`pane-tab-${next.id}`)?.focus());
  }
</script>

<div class="panes">
  <div class="bar">
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
          onclick={() => (active = p.id)}
          onkeydown={(e) => onkey(e, i)}
        >
          {p.label}{#if p.badge !== undefined && p.badge !== ''}<span class="badge">{p.badge}</span>{/if}
        </button>
      {/each}
    </div>
    <span class="grow"></span>
    {#if toolbar}<div class="tools">{@render toolbar()}</div>{/if}
  </div>
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
</style>
