<script lang="ts">
  // The context bar (right), a menu: the sections of the editor shown (bound to the change it shows; the project and
  // the working change are in the header), an icon each with its count. Closed, it is a rail of icons with its pin at the
  // top; open (a section clicked, the focus, or pinned beside the editor), it slides out and each icon gets its label.
  // Open over the editor, it follows the section hovered, as the navigation follows the tool hovered.
  import Icon from './Icon.svelte';
  import { layout } from './layout.svelte';
  import { compactBadge, contextBar, editorContext, paneIcon } from './context.svelte';

  let { open = false }: { open?: boolean } = $props();

  const ec = $derived(editorContext.current);

  function pin() {
    layout.rightOpen = !layout.rightOpen;
    contextBar.revealed = false;
  }

  function pick(id: string) {
    if (!contextBar.docked) contextBar.revealed = true;
    ec?.pick(id);
  }

  let hoverTimer: ReturnType<typeof setTimeout> | undefined;
  function hover(id: string) {
    clearTimeout(hoverTimer);
    if (contextBar.docked || !contextBar.revealed || ec?.active === id) return;
    hoverTimer = setTimeout(() => {
      if (contextBar.revealed && !contextBar.docked) ec?.pick(id);
    }, 120);
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

<aside class="ctx" class:open aria-label="Context">
  <div class="top">
    <button
      type="button"
      class="pin"
      class:on={layout.rightOpen}
      aria-pressed={layout.rightOpen}
      title={layout.rightOpen ? 'Unpin: the bar closes to its icons' : 'Pin the bar open beside the editor'}
      aria-label="Pin the context bar"
      onclick={pin}><Icon name="pin" size={14} /></button
    >
    <span class="title">Context</span>
  </div>

  {#if ec}
    <section class="secs" aria-label={ec.label}>
      <div class="head">{ec.label}</div>
      <!-- svelte-ignore a11y_interactive_supports_focus -->
      <div class="list" role="tablist" aria-label={ec.label} aria-orientation="vertical" onkeydown={keydown}>
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
            tabindex={on ? 0 : -1}
            title={p.title ?? (p.badge !== undefined && p.badge !== '' ? `${p.label} · ${p.badge}` : p.label)}
            onclick={() => pick(p.id)}
            onmouseenter={() => hover(p.id)}
            onmouseleave={() => clearTimeout(hoverTimer)}
          >
            <span class="ic">
              <Icon name={paneIcon(p)} size={16} />
              {#if count && !open}<span class="dot">{count}</span>{/if}
              {#if p.tint}<span class="tint" style="background: {p.tint}" aria-hidden="true"></span>{/if}
            </span>
            <span class="label">{p.label}</span>
            {#if p.badge !== undefined && p.badge !== ''}<span class="count">{p.badge}</span>{/if}
          </button>
        {/each}
      </div>
    </section>
  {/if}
</aside>

<style>
  .ctx {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    overflow-x: hidden;
    overflow-y: auto;
    background: var(--bg-panel);
    border-left: 1px solid var(--border);
    scrollbar-width: thin;
  }
  .top {
    display: flex;
    align-items: center;
    flex-direction: row-reverse;
    height: 36px;
    flex: none;
  }
  .title {
    flex: 1;
    padding-left: 0.8rem;
    font-size: var(--fs-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-3);
    white-space: nowrap;
    overflow: hidden;
  }
  .pin,
  .sec {
    border: none;
    border-radius: 0;
    background: none;
    color: var(--text-3);
    padding: 0;
    min-height: 0;
  }
  .pin {
    width: 43px;
    height: 36px;
    flex: none;
    display: grid;
    place-items: center;
  }
  .pin:hover:not(:disabled) {
    background: none;
    color: var(--text);
  }
  /* the pin: slanted while the bar closes to its rail, upright and accented while pinned */
  .pin :global(svg) {
    transform: rotate(45deg);
    transition: transform var(--transition);
  }
  .pin.on,
  .pin.on:hover:not(:disabled) {
    color: var(--accent);
  }
  .pin.on :global(svg) {
    transform: none;
  }
  .secs {
    padding: 6px 0;
  }
  .head {
    height: 22px;
    padding: 0 0.8rem;
    font-size: var(--fs-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-3);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    opacity: 0;
    transition: opacity 0.15s;
  }
  .open .head {
    opacity: 1;
  }
  .list {
    display: grid;
  }
  .sec {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    min-height: 36px;
    padding: 0 10px 0 12px;
    border-left: 2px solid transparent;
    text-align: left;
    white-space: nowrap;
    font-weight: 500;
  }
  .sec:hover:not(:disabled) {
    background: var(--hover);
    color: var(--text);
  }
  .sec.on {
    color: var(--text);
    border-left-color: var(--accent);
    background: var(--accent-soft, var(--hover));
  }
  .sec:focus-visible,
  .pin:focus-visible {
    outline-offset: -2px;
  }
  .ic {
    position: relative;
    flex: none;
    display: grid;
    place-items: center;
    width: 18px;
    height: 18px;
  }
  .dot {
    position: absolute;
    top: -7px;
    left: 7px;
    min-width: 15px;
    height: 15px;
    padding: 0 4px;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-text);
    font-size: 9.5px;
    font-weight: 700;
    line-height: 15px;
    text-align: center;
  }
  .tint {
    position: absolute;
    bottom: -2px;
    right: -3px;
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .count {
    flex: none;
    max-width: 45%;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text-3);
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
  }
  .sec.on .count {
    color: var(--accent);
  }
</style>
