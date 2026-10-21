<script lang="ts">
  // The context, in the header: where the person works (project, working change), then the sections of the editor
  // shown (bound to the change it shows), an icon each with its count; the one shown also has its label.
  import Icon from './Icon.svelte';
  import ProjectSelector from './ProjectSelector.svelte';
  import WorkingChangeSelector from './WorkingChangeSelector.svelte';
  import { compactBadge, editorContext, paneIcon } from './context.svelte';

  const ec = $derived(editorContext.current);

  function keydown(e: KeyboardEvent) {
    const step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    if (!step) return;
    const items = [...(e.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="tab"]')];
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    if (i < 0) return;
    const to = items[(i + step + items.length) % items.length];
    to?.focus();
    to?.click();
    e.preventDefault();
  }
</script>

<div class="hctx" aria-label="Context" role="group">
  <ProjectSelector />
  <WorkingChangeSelector />
  {#if ec}
    <span class="sep" aria-hidden="true"></span>
    <!-- svelte-ignore a11y_interactive_supports_focus -->
    <div class="secs" role="tablist" aria-label={ec.label} onkeydown={keydown}>
      {#each ec.panes as p (p.id)}
        {@const on = ec.active === p.id}
        {@const count = compactBadge(p.badge)}
        <button
          type="button"
          role="tab"
          id="ctx-tab-{p.id}"
          class="sec"
          class:on
          aria-selected={on}
          aria-controls="pane-{p.id}"
          aria-label={p.label}
          tabindex={on ? 0 : -1}
          title={p.title ?? (p.badge !== undefined && p.badge !== '' ? `${p.label} · ${p.badge}` : p.label)}
          onclick={() => ec.pick(p.id)}
        >
          <span class="ic">
            <Icon name={paneIcon(p)} size={15} />
            {#if count}<span class="dot">{count}</span>{/if}
            {#if p.tint}<span class="tint" style="background: {p.tint}" aria-hidden="true"></span>{/if}
          </span>
          {#if on}<span class="label">{p.label}</span>{/if}
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .hctx {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1 1 0;
    min-width: 0;
  }
  .sep {
    width: 1px;
    height: 18px;
    flex: none;
    margin: 0 4px;
    background: var(--border);
  }
  .secs {
    display: flex;
    align-items: center;
    gap: 1px;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
    padding: 6px 2px 2px;
  }
  .sec {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    flex: none;
    height: 28px;
    min-height: 0;
    padding: 0 7px;
    border: none;
    border-radius: var(--radius-m);
    background: none;
    color: var(--text-3);
    font-size: var(--fs-sm);
    font-weight: 600;
    white-space: nowrap;
  }
  .sec:hover:not(:disabled) {
    background: var(--hover);
    color: var(--text);
  }
  .sec.on {
    color: var(--text);
    background: var(--accent-soft, var(--hover));
    box-shadow: inset 0 -2px 0 var(--accent);
  }
  .sec:focus-visible {
    outline-offset: -2px;
  }
  .ic {
    position: relative;
    display: grid;
    place-items: center;
    width: 16px;
    height: 16px;
  }
  .dot {
    position: absolute;
    top: -7px;
    left: 9px;
    min-width: 14px;
    height: 14px;
    padding: 0 3px;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-text);
    font-size: 9px;
    font-weight: 700;
    line-height: 14px;
    text-align: center;
  }
  /* a narrow header: the project and the working change by their icons (their name in the tooltip) */
  @media (max-width: 1280px) {
    .hctx :global(.pl),
    .hctx :global(.wl) {
      display: none;
    }
  }
  .tint {
    position: absolute;
    bottom: -3px;
    right: -3px;
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }
</style>
