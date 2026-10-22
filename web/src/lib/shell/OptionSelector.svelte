<script lang="ts">
  // Option selector: the option of the working change the person works on (ADR 0032 §6), next to the working change
  // selector. 'Main flow' by default; picking an open option makes it the change's active option, and every screen then
  // sees and edits the change as that option has it (stores/workingChange).
  import Icon from './Icon.svelte';
  import Popover from './Popover.svelte';
  import { MAIN_SCOPE, scopeColor, scopeName } from '../changeScope';
  import { working, workOnOption } from '../stores/workingChange.svelte';

  let open = $state(false);
  let busy = $state(false);
  const label = $derived(scopeName(working.options, working.option));
  const tint = $derived(scopeColor(working.options, working.option));

  async function pick(o: string): Promise<void> {
    open = false;
    busy = true;
    try {
      await workOnOption(o);
    } finally {
      busy = false;
    }
  }
</script>

{#if working.change}
  <div class="item-wrap">
    <button
      type="button"
      class="oc"
      class:main={working.option === MAIN_SCOPE}
      style="--tint: {tint}"
      disabled={busy}
      aria-haspopup="dialog"
      aria-expanded={open}
      title={working.option === MAIN_SCOPE ? 'Working on the main flow of the change' : `Working on the option ${label}`}
      onclick={() => (open = !open)}
    >
      <Icon name="diff" size={14} />
      <span class="ol">{label}</span>
    </button>
    <Popover bind:open label="Option" align="left" placement="below" width="260px">
      <div class="pop-head"><strong>Option of the change</strong></div>
      <ul class="list">
        <li>
          <button type="button" class="row-btn" class:active={working.option === MAIN_SCOPE} onclick={() => pick(MAIN_SCOPE)}>Main flow</button>
        </li>
        {#each working.options as o (o.id)}
          <li>
            <button type="button" class="row-btn" class:active={working.option === o.id} onclick={() => pick(o.id ?? '')}>
              <i style="background: {scopeColor(working.options, o.id ?? '')}"></i>{o.option?.name ?? o.id}
              {#if o.optionStatus}<span class="hint"> {o.optionStatus}</span>{/if}
            </button>
          </li>
        {:else}
          <li><p class="pad hint">No open option: the change is worked on its main flow.</p></li>
        {/each}
      </ul>
    </Popover>
  </div>
{/if}

<style>
  .item-wrap {
    position: relative;
    display: flex;
  }
  .oc {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.25rem 0.6rem;
    border: 1px solid var(--tint);
    border-radius: var(--radius-m);
    background: var(--bg-raised);
    color: inherit;
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
  .oc.main {
    border-color: var(--border);
  }
  .oc:hover:not(:disabled) {
    border-color: var(--border-strong);
  }
  .ol {
    max-width: 10rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .pop-head {
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0.2rem 0;
  }
  .row-btn {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    width: 100%;
    border: none;
    border-radius: 0;
    background: none;
    text-align: left;
    font-weight: 400;
    padding: 0.3rem 0.6rem;
    min-height: 0;
  }
  .row-btn:hover {
    background: var(--hover);
  }
  .row-btn.active {
    font-weight: 600;
  }
  i {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }
  .pad {
    padding: 0.4rem 0.6rem;
    margin: 0;
  }
</style>
