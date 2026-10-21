<script lang="ts">
  // Navigation sidebar: where the person works (project, working change), the tools of the left area, named and counted, then the tool shown (the console keeps its
  // own tabs and counts).
  // Folded away (Ctrl+B), the activity bar's icons stand in for it.
  import Icon from './Icon.svelte';
  import { panelView, viewsIn } from './registry';
  import { layout, showTool } from './layout.svelte';
  import type { PanelView } from './types';
  import ProjectSelector from './ProjectSelector.svelte';
  import WorkingChangeSelector from './WorkingChangeSelector.svelte';

  const tools = $derived(viewsIn('left'));
  const view = $derived(panelView(layout.left));

  const sections = $derived([
    { id: 'tools', title: 'Workbench', views: tools, on: (v: PanelView) => layout.left === v.id, pick: (v: PanelView) => showTool('left', v.id) },
  ]);

  function fold(id: string) {
    layout.navFolded = layout.navFolded.includes(id) ? layout.navFolded.filter((x) => x !== id) : [...layout.navFolded, id];
  }

  function keydown(e: KeyboardEvent) {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const items = [...(e.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('.ui-nav-item')];
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    items[(i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus();
    e.preventDefault();
  }
</script>

<section class="sidebar" aria-label="Navigation">
  <div class="where" aria-label="Where you work">
    <div class="ui-section-head">Project</div>
    <ProjectSelector block />
    <WorkingChangeSelector block />
  </div>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <nav class="nav" aria-label="Tools" onkeydown={keydown}>
    {#each sections as sec (sec.id)}
      {@const folded = layout.navFolded.includes(sec.id)}
      <div class="sec">
        <button type="button" class="ui-section-head head" aria-expanded={!folded} onclick={() => fold(sec.id)}>
          <span class="chev" class:folded><Icon name="chevronDown" size={11} /></span>{sec.title}
        </button>
        {#if !folded}
          <ul>
            {#each sec.views as v (v.id)}
              {@const on = sec.on(v)}
              {@const badge = v.badge?.()}
              <li>
                <button type="button" class="ui-nav-item" class:active={on} aria-current={on ? 'true' : undefined} aria-label={badge ? `${v.title}, ${badge}` : v.title} onclick={() => sec.pick(v)}>
                  <Icon name={v.icon} size={14} />
                  <span class="label">{v.title}</span>
                  {#if badge}<span class="count" aria-hidden="true">{badge}</span>{/if}
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    {/each}
  </nav>

  <header class="tool-head">
    <h2>{view?.title ?? ''}</h2>
    <button type="button" class="ghost small icon" title="Hide the navigation (Ctrl+B)" aria-label="Hide the navigation" onclick={() => (layout.leftOpen = false)}>
      <Icon name="x" size={14} />
    </button>
  </header>
  <div class="body">
    {#if view}
      {@const C = view.component}
      <C />
    {/if}
  </div>
</section>

<style>
  .sidebar {
    container: sidebar / inline-size;
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    background: var(--bg-panel);
  }
  .where {
    flex: none;
    display: grid;
    gap: 6px;
    padding: var(--space-2) var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--border);
  }
  .where .ui-section-head {
    padding: 0 0 0 2px;
  }
  .nav {
    flex: none;
    max-height: 48%;
    overflow: auto;
    padding: var(--space-2) var(--space-2) var(--space-1);
    border-bottom: 1px solid var(--border);
  }
  .sec + .sec {
    margin-top: var(--space-1);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 4px;
    width: 100%;
    min-height: 0;
    border: none;
    background: none;
    text-align: left;
    cursor: pointer;
  }
  .head:hover:not(:disabled) {
    background: none;
    color: var(--text-2);
  }
  .chev {
    display: inline-flex;
    transition: transform var(--transition);
  }
  .chev.folded {
    transform: rotate(-90deg);
  }
  ul {
    list-style: none;
    margin: 0 0 var(--space-1);
    padding: 0;
    display: grid;
    gap: 1px;
  }
  /* a wide sidebar lays the tools out in two columns, leaving the height to the tool shown */
  @container sidebar (min-width: 250px) {
    ul {
      grid-template-columns: 1fr 1fr;
      column-gap: 2px;
    }
  }
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .count {
    min-width: 18px;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--bg-inset);
    border: 1px solid var(--border);
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    line-height: 16px;
    text-align: center;
  }
  .ui-nav-item.active .count {
    border-color: var(--accent);
    color: var(--accent);
  }
  .tool-head {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    height: 32px;
    padding: 0 0.3rem 0 0.8rem;
    flex: none;
  }
  h2 {
    flex: 1;
    margin: 0;
    font-size: var(--fs-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-3);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .icon {
    padding: 0.15rem;
    color: var(--text-3);
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
</style>
