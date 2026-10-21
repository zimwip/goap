<script lang="ts">
  // The context bar (right): where the person works (project, working change), the context of what the editor shows
  // (its sections, bound to the change shown or else to the working change, and what waits on people), then the side
  // tools (the assistant's conversation, Test, Info, Help). Pinned, it is docked beside the editor; else it hides on
  // the edge and shows over the editor while hovered or focused.
  import Icon from './Icon.svelte';
  import ProjectSelector from './ProjectSelector.svelte';
  import WorkingChangeSelector from './WorkingChangeSelector.svelte';
  import { panelView, viewsIn } from './registry';
  import { layout } from './layout.svelte';
  import { openTab } from './tabs.svelte';
  import { CHANGE_SECTIONS, contextBar, editorContext } from './context.svelte';
  import { working } from '../stores/workingChange.svelte';
  import { assistant, closePanel, openPanel } from '../stores/assistant.svelte';
  import { assistantEnabled } from '../assistant/enabled';
  import { offerDock } from '../assistant/dock.svelte';
  import { shortId } from '../api';

  const ASSISTANT = 'assistant';
  const ec = $derived(editorContext.current);
  const w = $derived(working.change);
  // the working change's sections, when the editor does not show that change
  const workingSections = $derived(w && ec?.changeId !== w.id);
  const enabled = $derived(assistantEnabled());

  const tools = $derived([
    ...(enabled ? [{ id: ASSISTANT, title: 'Assistant', icon: 'chat' as const }] : []),
    ...viewsIn('right').map((v) => ({ id: v.id, title: v.title, icon: v.icon })),
  ]);
  const tool = $derived(tools.find((t) => t.id === layout.right) ?? tools[0]);
  const view = $derived(tool && tool.id !== ASSISTANT ? panelView(tool.id) : undefined);

  // the bubble opened the conversation: it shows here
  $effect(() => {
    if (assistant.panelOpen && enabled) layout.right = ASSISTANT;
  });

  function pick(id: string) {
    layout.right = id;
    if (id === ASSISTANT) openPanel();
    else if (assistant.panelOpen) closePanel();
  }

  function openSection(changeId: string, pane: string) {
    openTab({ kind: 'change', params: { id: changeId, pane } }, { pin: true });
  }

  function pin() {
    layout.rightOpen = !layout.rightOpen;
    contextBar.revealed = false;
  }

  function keydown(e: KeyboardEvent) {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const items = [...(e.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="tab"]')];
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    if (i < 0) return;
    items[(i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus();
    e.preventDefault();
  }
</script>

<aside class="ctx" aria-label="Context">
  <header class="top">
    <h2>Context</h2>
    <button
      type="button"
      class="ghost small icon"
      class:on={layout.rightOpen}
      aria-pressed={layout.rightOpen}
      title={layout.rightOpen ? 'Unpin: the bar hides on the edge' : 'Pin the bar beside the editor'}
      aria-label="Pin the context bar"
      onclick={pin}><Icon name="pin" size={14} /></button
    >
  </header>

  <div class="upper">
    <section class="where" aria-label="Where you work">
      <div class="ui-section-head">Project</div>
      <ProjectSelector block />
      <WorkingChangeSelector block />
    </section>

    {#if ec}
      <section aria-label={ec.label}>
        <div class="ui-section-head">{ec.label}</div>
        <!-- svelte-ignore a11y_interactive_supports_focus -->
        <div class="list" role="tablist" aria-label={ec.label} aria-orientation="vertical" onkeydown={keydown}>
          {#each ec.panes as p (p.id)}
            {@const on = ec.active === p.id}
            <button
              type="button"
              role="tab"
              id="ctx-tab-{p.id}"
              class="ui-nav-item"
              class:active={on}
              aria-selected={on}
              tabindex={on ? 0 : -1}
              title={p.title}
              onclick={() => ec.pick(p.id)}
            >
              {#if p.tint}<span class="tint" style="background: {p.tint}" aria-hidden="true"></span>{/if}
              <span class="label">{p.label}</span>
              {#if p.badge !== undefined && p.badge !== ''}<span class="count">{p.badge}</span>{/if}
            </button>
          {/each}
        </div>
      </section>
      {#if contextBar.docked && ec.aside}<div class="aside">{@render ec.aside()}</div>{/if}
    {/if}

    {#if workingSections && w}
      <section aria-label="Working change">
        <div class="ui-section-head" title={w.title}>Working change · {w.title || shortId(w.id)}</div>
        <ul class="list">
          {#each CHANGE_SECTIONS as s (s.id)}
            <li><button type="button" class="ui-nav-item" onclick={() => openSection(w.id ?? '', s.id)}><span class="label">{s.label}</span></button></li>
          {/each}
        </ul>
      </section>
    {/if}
  </div>

  {#if tools.length}
    <div class="tools">
      <div class="strip" role="tablist" aria-label="Side tools">
        {#each tools as t (t.id)}
          {@const on = tool?.id === t.id}
          <button type="button" role="tab" class:on aria-selected={on} title={t.title} onclick={() => pick(t.id)}>
            <Icon name={t.icon} size={13} /><span class="tl">{t.title}</span>
          </button>
        {/each}
      </div>
      <div class="body" role="tabpanel" aria-label={tool?.title}>
        {#if tool?.id === ASSISTANT}
          <!-- the assistant's conversation docks here (its bubble opens it) -->
          <div class="dock" use:offerDock></div>
          {#if !assistant.panelOpen}
            <div class="ui-empty">
              <button type="button" class="ui-btn primary" onclick={openPanel}><Icon name="chat" size={14} />Ask the assistant</button>
            </div>
          {/if}
        {:else if view}
          {@const C = view.component}
          <C />
        {/if}
      </div>
    </div>
  {/if}
</aside>

<style>
  .ctx {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    background: var(--bg-panel);
  }
  .top {
    display: flex;
    align-items: center;
    height: 32px;
    padding: 0 0.3rem 0 0.8rem;
    flex: none;
    border-bottom: 1px solid var(--border);
  }
  h2 {
    flex: 1;
    margin: 0;
    font-size: var(--fs-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-3);
  }
  .icon {
    padding: 0.2rem;
    color: var(--text-3);
  }
  .icon.on {
    color: var(--accent);
  }
  .upper {
    flex: 0 1 auto;
    max-height: 58%;
    overflow: auto;
    padding: var(--space-2);
    display: grid;
    gap: var(--space-3);
    align-content: start;
  }
  .where {
    display: grid;
    gap: 6px;
  }
  .where .ui-section-head,
  section > .ui-section-head {
    padding: 0 0 4px 2px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .list {
    display: grid;
    gap: 1px;
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tint {
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }
  .count {
    color: var(--text-3);
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
  }
  .ui-nav-item.active .count {
    color: var(--accent);
  }
  .tools {
    flex: 1 1 240px;
    min-height: 200px;
    display: flex;
    flex-direction: column;
    border-top: 1px solid var(--border);
  }
  .strip {
    display: flex;
    flex: none;
    gap: 2px;
    padding: 0 var(--space-2);
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
    scrollbar-width: none;
  }
  .strip button {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    min-height: 30px;
    padding: 0 8px;
    border: none;
    border-radius: 0;
    background: none;
    color: var(--text-3);
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    white-space: nowrap;
  }
  .strip button:hover:not(:disabled) {
    background: none;
    color: var(--text);
  }
  .strip button.on {
    color: var(--text);
    box-shadow: inset 0 -2px 0 var(--accent);
  }
  .strip button:focus-visible {
    outline-offset: -2px;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    display: flex;
    flex-direction: column;
  }
  .dock {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .dock:empty {
    display: none;
  }
  .dock :global(.panel) {
    border: none;
    border-radius: 0;
  }
</style>
