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
    /** the icon of the section in the header's context (else the one of its id) */
    icon?: import('../shell/Icon.svelte').IconName;
  }
</script>

<script lang="ts">
  // The IDE's multi-pane editor: a main object opens in one editor tab and its
  // information is organized in sections (panes). The active pane is remembered
  // by the caller (e.g. in the tab params) through `active`. The sections are listed by the context of the header
  // (an icon each, with its count), never as tabs of the editor.
  import type { Snippet } from 'svelte';
  import { publishContext, withdrawContext } from '../shell/context.svelte';

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
    /** the change these are the sections of (the change editor): the header's context binds them to it */
    changeId?: string;
    /** content on the right, above the pane (actions, working change…) */
    toolbar?: Snippet;
    /** what comes before the panes (the object's scope, its lifecycle…) */
    head?: Snippet;
    /** what comes above the pane, under the head (what waits for people…) */
    aside?: Snippet;
    /** renders the pane with the given id */
    children: Snippet<[string]>;
  } = $props();

  const visible = $derived(panes.filter((p) => !p.hidden));
  const current = $derived(visible.find((p) => p.id === active)?.id ?? visible[0]?.id ?? '');

  // the header's context shows what this editor holds, while it is shown
  const owner = Symbol('panes');
  $effect(() => {
    publishContext({ owner, label, panes: visible, active: current, pick: (id) => (active = id), changeId });
  });
  $effect(() => () => withdrawContext(owner));
</script>

<div class="panes">
  {#if head}<div class="head">{@render head()}</div>{/if}
  {#if toolbar}
    <div class="bar">
      <span class="grow"></span>
      <div class="tools">{@render toolbar()}</div>
    </div>
  {/if}
  {#if aside}<div class="aside-top">{@render aside()}</div>{/if}
  <div class="body" role="region" id="pane-{current}" aria-label={visible.find((p) => p.id === current)?.label}>
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
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.8rem;
    flex-wrap: wrap;
  }
  .tools {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    flex-wrap: wrap;
  }
  .aside-top {
    margin-bottom: 0.8rem;
  }
  .body {
    min-width: 0;
  }
</style>
