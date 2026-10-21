<script lang="ts">
  // The actions of an editor, in the editor itself (at the end of its heading): the primary ones and the first others
  // as buttons, the rest in a "More" menu. The editor gives them with `provideActions`; the shortcuts run them too.
  import Icon from './Icon.svelte';
  import Popover from './Popover.svelte';
  import { tabActions } from './workbench.svelte';
  import type { ToolbarAction } from './types';

  let { tabId }: { tabId: string } = $props();

  const actions = $derived<ToolbarAction[]>(tabActions.get(tabId) ?? []);
  const SHOWN = 2;
  const secondary = $derived(actions.filter((a) => !a.primary));
  const shown = $derived(actions.filter((a) => a.primary || secondary.indexOf(a) < SHOWN));
  const more = $derived(secondary.slice(SHOWN));
  let moreOpen = $state(false);

  let running = $state('');
  async function run(a: ToolbarAction) {
    running = a.id;
    try {
      await a.run();
    } finally {
      running = '';
    }
  }
</script>

<div class="toolbar" role="toolbar" aria-label="Actions">
  <div class="group context">
    {#each shown as a (a.id)}
      <button
        type="button"
        class="tb"
        class:primary={a.primary}
        class:danger={a.danger}
        disabled={a.disabled || running === a.id}
        title={`${a.title ?? a.label}${a.shortcut ? ` (${a.shortcut})` : ''}`}
        onclick={() => run(a)}
      >
        {#if a.icon}<Icon name={a.icon} size={14} />{/if}
        <span class="lbl">{a.label}</span>
      </button>
    {/each}
  </div>
  {#if more.length}
    <div class="more">
      <button type="button" class="tb" aria-haspopup="menu" aria-expanded={moreOpen} title="More actions" aria-label="More actions" onclick={() => (moreOpen = !moreOpen)}>
        <Icon name="more" size={14} />
      </button>
      <Popover bind:open={moreOpen} label="More actions" align="right" placement="below" width="220px">
        <ul class="menu" role="menu">
          {#each more as a (a.id)}
            <li role="none">
              <button
                type="button"
                role="menuitem"
                class="mi"
                class:danger={a.danger}
                disabled={a.disabled || running === a.id}
                title={`${a.title ?? a.label}${a.shortcut ? ` (${a.shortcut})` : ''}`}
                onclick={() => ((moreOpen = false), run(a))}
              >
                {#if a.icon}<Icon name={a.icon} size={14} />{/if}{a.label}
              </button>
            </li>
          {/each}
        </ul>
      </Popover>
    </div>
  {/if}
</div>

<style>
  .toolbar {
    display: flex;
    margin-left: auto;
    flex: none;
    max-width: 100%;
    align-items: center;
    min-width: 0;
    overflow: hidden;
  }
  .group {
    display: flex;
    align-items: center;
    gap: 2px;
    min-width: 0;
  }
  .context {
    gap: 6px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tb {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    flex: none;
    min-height: 26px;
    padding: 0.1rem 0.6rem;
    font-size: var(--fs-sm);
    white-space: nowrap;
  }
  .more {
    position: relative;
    display: flex;
    margin-left: 6px;
  }
  .menu {
    list-style: none;
    margin: 0;
    padding: 4px;
  }
  .mi {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    border: none;
    background: none;
    text-align: left;
    font-weight: 400;
    padding: 0.35rem 0.5rem;
  }
  .mi:hover:not(:disabled) {
    background: var(--hover);
  }
  .tb.danger:not(:hover) {
    border-color: var(--border);
  }
  @media (max-width: 1100px) {
    .tb:not(.primary) .lbl {
      display: none;
    }
  }
</style>
