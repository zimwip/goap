<script lang="ts">
  // "Tasks" tool (ADR 0158): what the open changes wait for from people, as the person's work. Listed at three levels:
  // the person's tasks across changes (accepted, or addressed to them alone), the tasks of the working change, the
  // tasks of the changes open in an editor; in project mode, the tasks of every change of the project. Picking a task
  // works on it: its change becomes the working change and the front enters change mode on it.
  import Icon from '../../shell/Icon.svelte';
  import { type ChangeAsk } from '../../api';
  import { tabsState } from '../../shell/tabs.svelte';
  import { mode } from '../../shell/mode.svelte';
  import { me } from '../../stores/session.svelte';
  import { project } from '../../stores/project.svelte';
  import { working } from '../../stores/workingChange.svelte';
  import { accept, accepted, followTasks, reloadTasks, tasks, workOnTask } from '../../stores/tasks.svelte';
  import { acceptable, byChange, isMine, myTasks, taskKey, tasksOf } from '../../tasks';
  import { addressee, askKind, waitingFor } from '../../changeAsks';

  $effect(() => followTasks());

  const subject = $derived(me());
  const acc = $derived(accepted());
  const mine = $derived(myTasks(tasks.asks, subject, acc));
  const current = $derived(working.change ? tasksOf(tasks.asks, working.change.id) : []);
  /** the changes open in an editor, other than the working one */
  const opened = $derived(
    [...new Set(tabsState.tabs.filter((t) => t.kind === 'change' && t.params.id && t.params.id !== working.change?.id).map((t) => t.params.id))]
      .map((id) => ({ id, asks: tasksOf(tasks.asks, id) }))
      .filter((g) => g.asks.length),
  );
  const ofProject = $derived(mode.current === 'project' ? byChange(tasks.asks, project.current || undefined) : []);

  let busy = $state('');
  async function work(a: ChangeAsk) {
    busy = a.id ?? '';
    try {
      await workOnTask(a);
    } finally {
      busy = '';
    }
  }
</script>

{#snippet row(a: ChangeAsk, showChange: boolean)}
  {@const own = isMine(a, subject, acc)}
  <li class:own>
    <button type="button" class="task" disabled={!!busy} title="Work on this task: its change becomes the working change" onclick={() => void work(a)}>
      <span class="line1">
        <span class="kind">{askKind(a)}</span>
        <span class="title">{a.title || a.action || a.id}</span>
      </span>
      <span class="line2">
        {#if showChange}<span class="change"><Icon name="diff" size={11} />{a.changeTitle || a.changeId}</span> · {/if}for {addressee(a, subject)}{#if waitingFor(a.since)} · {waitingFor(a.since)}{/if}
      </span>
    </button>
    {#if acceptable(a, subject, acc)}
      <button type="button" class="ghost small take" title="Accept this task: it joins your tasks" onclick={() => accept(a)}>Accept</button>
    {:else if own && a.roles?.length}
      <button type="button" class="ghost small take" title="Give this task back: it leaves your tasks" onclick={() => accept(a, false)}>Give back</button>
    {/if}
  </li>
{/snippet}

<div class="explorer">
  <div class="tools">
    <span class="grow hint">{tasks.loaded ? '' : 'Reading the tasks…'}</span>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" onclick={() => void reloadTasks()}><Icon name="refresh" size={14} /></button>
  </div>
  {#if tasks.error}<div class="alert small">{tasks.error}</div>{/if}

  <section aria-label="My tasks">
    <h3 class="ui-section-head">My tasks <span class="count">{mine.length}</span></h3>
    {#if mine.length}
      <ul class="list">{#each mine as a (taskKey(a))}{@render row(a, true)}{/each}</ul>
    {:else if tasks.loaded}
      <p class="empty">Nothing is addressed to you alone. Accept a task below to make it yours.</p>
    {/if}
  </section>

  {#if working.change}
    <section aria-label="Tasks of the working change">
      <h3 class="ui-section-head" title={working.change.title}>This change <span class="count">{current.length}</span></h3>
      {#if current.length}
        <ul class="list">{#each current as a (a.id)}{@render row(a, false)}{/each}</ul>
      {:else if tasks.loaded}
        <p class="empty">“{working.change.title}” waits for no one.</p>
      {/if}
    </section>
  {/if}

  {#each opened as g (g.id)}
    <section aria-label="Tasks of a change open in an editor">
      <h3 class="ui-section-head" title="Open in an editor">{g.asks[0].changeTitle || g.id} <span class="count">{g.asks.length}</span></h3>
      <ul class="list">{#each g.asks as a (a.id)}{@render row(a, false)}{/each}</ul>
    </section>
  {/each}

  {#if mode.current === 'project'}
    <section aria-label="Tasks of the project">
      <h3 class="ui-section-head">Project <span class="count">{ofProject.reduce((n, g) => n + g.asks.length, 0)}</span></h3>
      {#each ofProject as g (g.changeId)}
        <h4 title={g.changeId}><Icon name="diff" size={12} />{g.title}</h4>
        <ul class="list">{#each g.asks as a (a.id)}{@render row(a, false)}{/each}</ul>
      {:else}
        {#if tasks.loaded}<p class="empty">No open change of the project waits on a person.</p>{/if}
      {/each}
    </section>
  {/if}
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 0 0.5rem 0.2rem;
  }
  .grow {
    flex: 1;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0.6rem 0 0.2rem;
    padding: 0 0.7rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  h4 {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    margin: 0.4rem 0 0.1rem;
    padding: 0 0.7rem;
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-2);
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
  .empty {
    margin: 0;
    padding: 0.2rem 0.8rem;
    font-size: 0.88em;
    color: var(--muted);
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .list li {
    display: flex;
    align-items: center;
    gap: 0.2rem;
    padding-right: 0.4rem;
    border-bottom: 1px solid var(--border);
  }
  .list li.own {
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .task {
    flex: 1;
    min-width: 0;
    display: grid;
    gap: 1px;
    padding: 0.3rem 0.4rem 0.3rem 0.7rem;
    border: none;
    border-radius: 0;
    background: none;
    text-align: left;
    font-weight: 400;
    min-height: 0;
  }
  .task:hover:not(:disabled) {
    background: var(--hover);
  }
  .line1 {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
    min-width: 0;
  }
  .kind {
    flex: none;
    font-size: var(--fs-xs);
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: var(--accent);
  }
  .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 600;
  }
  .line2 {
    font-size: 0.85em;
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .change {
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }
  .take {
    flex: none;
    font-size: var(--fs-xs);
  }
</style>
