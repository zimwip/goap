<script lang="ts">
  // Project selector (ADR 0039): the project the user is working on, selected before any non-administrative
  // action. Sits between the live indicator and the notification bell. None is selected at first: the user (or the
  // assistant) picks one. Switching it reissues the token (project store) so every call from here on carries it.
  import Icon from './Icon.svelte';
  import Popover from './Popover.svelte';
  import { project, refreshProjects, selectProject } from '../stores/project.svelte';

  /** full width, in the navigation sidebar (else a chip of the header) */
  let { block = false }: { block?: boolean } = $props();
  let open = $state(false);

  $effect(() => {
    if (open && !project.options.length && !project.loading) void refreshProjects();
  });

  const label = $derived(project.current ? project.options.find((o) => o.key === project.current)?.label || project.current : 'Select a project');

  async function pick(key: string): Promise<void> {
    open = false;
    await selectProject(key);
  }
</script>

<div class="item-wrap" class:block>
  <button type="button" class="proj" class:none={!project.current} aria-haspopup="dialog" aria-expanded={open} title={project.current ? `Active project: ${project.current}` : 'No project selected: pick the project you work in'} onclick={() => (open = !open)}>
    <Icon name="diff" size={14} />
    <span class="pl">{label}</span>
  </button>
  <Popover bind:open label="Project" align="left" placement="below" width="260px">
    <div class="pop-head">
      <strong>Project</strong>
      <span class="grow"></span>
      <button type="button" class="small ghost" disabled={project.loading} onclick={() => refreshProjects()}><Icon name="refresh" size={13} /></button>
    </div>
    {#if project.error}<p class="pad hint">{project.error}</p>{/if}
    <ul class="list">
      {#each project.options as o (o.key)}
        <li>
          <button type="button" class="row-btn" class:active={project.current === o.key} onclick={() => pick(o.key)}>
            <span class="main">{o.label}<span class="hint el"> {o.key}</span></span>
          </button>
        </li>
      {:else}
        {#if !project.loading}<li><p class="pad hint">No project yet.</p></li>{/if}
      {/each}
      {#if project.current}
        <li class="sep">
          <button type="button" class="row-btn" onclick={() => pick('')}>
            <span class="main hint">Work outside any project</span>
          </button>
        </li>
      {/if}
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
  .block .proj {
    width: 100%;
  }
  .block .pl {
    flex: 1;
    max-width: none;
    text-align: left;
  }
  .proj {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.25rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    background: var(--bg-raised);
    color: inherit;
    font: inherit;
    font-family: var(--font-mono);
    font-size: 12px;
    cursor: pointer;
  }
  .proj.none {
    border-style: dashed;
    background: transparent;
    color: var(--muted);
  }
  .proj:hover {
    border-color: var(--border-strong);
  }
  .pl {
    max-width: 12rem;
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
  .sep {
    border-top: 1px solid var(--border);
    margin-top: 0.2rem;
    padding-top: 0.2rem;
  }
  .el {
    white-space: nowrap;
  }
</style>
