<script lang="ts">
  // Project selector (ADR 0039): the project the user is working on, selected before any non-administrative
  // action. Sits between the live indicator and the notification bell. Switching it reissues the token
  // (project store) so every call from here on carries it.
  import Icon from './Icon.svelte';
  import Popover from './Popover.svelte';
  import { project, refreshProjects, selectProject } from '../stores/project.svelte';

  let open = $state(false);

  $effect(() => {
    if (open && !project.options.length && !project.loading) void refreshProjects();
  });

  const label = $derived(project.options.find((o) => o.key === project.current)?.label || project.current || 'Root project');

  async function pick(key: string): Promise<void> {
    open = false;
    await selectProject(key);
  }
</script>

<div class="item-wrap">
  <button type="button" class="proj" aria-haspopup="dialog" aria-expanded={open} title="Active project" onclick={() => (open = !open)}>
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
      <li>
        <button type="button" class="row-btn" class:active={!project.current} onclick={() => pick('')}>
          <span class="main"><strong>Root project</strong></span>
        </button>
      </li>
      {#each project.options as o (o.key)}
        <li>
          <button type="button" class="row-btn" class:active={project.current === o.key} onclick={() => pick(o.key)}>
            <span class="main">{o.label}<span class="hint el"> {o.key}</span></span>
          </button>
        </li>
      {:else}
        {#if !project.loading}<li><p class="pad hint">No project yet.</p></li>{/if}
      {/each}
    </ul>
  </Popover>
</div>

<style>
  .item-wrap {
    position: relative;
    display: flex;
  }
  .proj {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.25rem 0.55rem;
    border: 1px solid var(--border, #8884);
    border-radius: 6px;
    background: transparent;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
  .proj:hover {
    background: var(--hover, #8882);
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
  .el {
    white-space: nowrap;
  }
</style>
