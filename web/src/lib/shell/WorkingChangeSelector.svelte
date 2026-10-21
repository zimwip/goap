<script lang="ts">
  // Working change selector: the change the person works in, within the active project (stores/workingChange). Sits
  // next to the project selector; it offers the open changes of the active project, opens the working change and
  // leaves it. With no project selected there is no change to work in: the project comes first.
  import Icon from './Icon.svelte';
  import Popover from './Popover.svelte';
  import { shortId } from '../api';
  import { changes, refreshChanges } from '../stores/catalog.svelte';
  import { project } from '../stores/project.svelte';
  import { rootProject } from '../stores/session.svelte';
  import { stopWorking, workIn, workable, working } from '../stores/workingChange.svelte';
  import { openTab } from './tabs.svelte';

  /** full width, in the navigation sidebar (else a chip of the header) */
  let { block = false }: { block?: boolean } = $props();
  let open = $state(false);

  $effect(() => {
    if (open && !changes.loaded && !changes.loading) void refreshChanges();
  });

  // the open changes of the active project, the most recent first as the catalog lists them
  const offered = $derived(project.current ? changes.items.filter((c) => workable(c) && (c.projectId || rootProject()) === project.current) : []);
  const w = $derived(working.change);
  const label = $derived(w ? w.title || shortId(w.id) : project.current ? 'No working change' : 'No change');

  async function pick(id: string): Promise<void> {
    open = false;
    const c = changes.items.find((x) => x.id === id);
    if (c) await workIn(c);
  }

  function show(): void {
    open = false;
    if (w) openTab({ kind: 'change', params: { id: w.id } }, { pin: true });
  }

  function leave(): void {
    open = false;
    stopWorking();
  }
</script>

<div class="item-wrap" class:block>
  <button
    type="button"
    class="wc"
    class:none={!w}
    disabled={!project.current}
    aria-haspopup="dialog"
    aria-expanded={open}
    title={!project.current ? 'Select a project first: the working change is one of its changes' : w ? `Working change: ${w.title} (${w.status})` : 'No working change: pick the change you work in'}
    onclick={() => (open = !open)}
  >
    <Icon name="branch" size={14} />
    <span class="wl">{label}</span>
  </button>
  <Popover bind:open label="Working change" align="left" placement="below" width="300px">
    <div class="pop-head">
      <strong>Working change</strong>
      <span class="grow"></span>
      <button type="button" class="small ghost" title="Reload the changes" disabled={changes.loading} onclick={() => refreshChanges()}><Icon name="refresh" size={13} /></button>
    </div>
    {#if w}
      <div class="current">
        <span class="main"><strong>{w.title}</strong><span class="hint el"> {w.status}</span></span>
        <div class="row">
          <button type="button" class="small" onclick={show}>Open</button>
          <button type="button" class="small" onclick={leave}>Stop working in it</button>
        </div>
      </div>
    {/if}
    {#if changes.error}<p class="pad hint">{changes.error}</p>{/if}
    <ul class="list">
      {#each offered as c (c.id)}
        <li>
          <button type="button" class="row-btn" class:active={w?.id === c.id} onclick={() => pick(c.id ?? '')}>
            <span class="main">{c.title || shortId(c.id ?? '')}<span class="hint el"> {c.status}</span></span>
          </button>
        </li>
      {:else}
        {#if !changes.loading}<li><p class="pad hint">No open change in this project: create one, or ask the assistant.</p></li>{/if}
      {/each}
    </ul>
  </Popover>
</div>

<style>
  .item-wrap {
    position: relative;
    display: flex;
  }
  .item-wrap.block {
    display: block;
  }
  .block .wc {
    width: 100%;
  }
  .block .wl {
    flex: 1;
    max-width: none;
    text-align: left;
  }
  .wc {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.25rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    background: var(--bg-raised);
    color: inherit;
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
  .wc.none {
    border-style: dashed;
    background: transparent;
    color: var(--muted);
  }
  .wc:hover:not(:disabled) {
    border-color: var(--border-strong);
  }
  .wc:disabled {
    cursor: default;
    opacity: 0.6;
  }
  .wl {
    max-width: 14rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .pop-head {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    background: var(--surface);
  }
  .current {
    display: grid;
    gap: 0.35rem;
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  .row {
    display: flex;
    gap: 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.6rem;
    margin: 0;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0.2rem 0;
  }
  .row-btn {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    border: none;
    border-radius: 0;
    background: none;
    text-align: left;
    font-weight: 400;
    padding: 0.3rem 0.6rem;
    min-height: 0;
  }
  .row-btn:hover:not(:disabled) {
    background: var(--hover);
  }
  .row-btn.active {
    font-weight: 600;
  }
  .main {
    flex: 1;
    min-width: 0;
    display: grid;
  }
  .el {
    white-space: nowrap;
  }
</style>
