<script lang="ts">
  // "Tasks" dashboard (ADR 0158): what the open changes wait for from people, as the person's work, in the editor space
  // (a tool that fills it, with no navigation). One area per list: the person's tasks across changes (held by them, or
  // addressed to them alone), the tasks available to them (they may answer them, nobody holds them), the tasks of the
  // working change, of the changes open in an editor and of every change of the project. Picking a task works on it:
  // its change becomes the working change and the front enters change mode on it, back in the editors. Each task has its
  // screen (who it is for, who holds it, accept, turn down, give to someone for an administrator of its project).
  import type { Snippet } from 'svelte';
  import Icon, { type IconName } from '../../shell/Icon.svelte';
  import { errorMessage, type ChangeAsk } from '../../api';
  import { tabsState } from '../../shell/tabs.svelte';
  import { layout } from '../../shell/layout.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import { me } from '../../stores/session.svelte';
  import { project } from '../../stores/project.svelte';
  import { working } from '../../stores/workingChange.svelte';
  import { accept, followTasks, openTask, reject, reloadTasks, tasks, workOnTask } from '../../stores/tasks.svelte';
  import { availableTasks, byChange, heldByOther, holderLabel, isMine, myTasks, taskKey, tasksOf } from '../../tasks';
  import { addressee, askKind, waitingFor } from '../../changeAsks';

  $effect(() => followTasks());

  const subject = $derived(me());
  const mine = $derived(myTasks(tasks.asks, subject));
  const offered = $derived(availableTasks(tasks.asks, subject));
  const current = $derived(working.change ? tasksOf(tasks.asks, working.change.id) : []);
  /** the changes open in an editor, other than the working one */
  const opened = $derived(
    [...new Set(tabsState.tabs.filter((t) => t.kind === 'change' && t.params.id && t.params.id !== working.change?.id).map((t) => t.params.id))]
      .map((id) => ({ id, title: tasksOf(tasks.asks, id)[0]?.changeTitle || id, asks: tasksOf(tasks.asks, id) }))
      .filter((g) => g.asks.length),
  );
  const ofProject = $derived(byChange(tasks.asks, project.current || undefined));
  const projectLabel = $derived(project.options.find((o) => o.key === project.current)?.label || project.current);
  const count = (groups: { asks: ChangeAsk[] }[]) => groups.reduce((n, g) => n + g.asks.length, 0);

  let busy = $state('');
  async function run(a: ChangeAsk, what: () => Promise<unknown>) {
    busy = taskKey(a);
    try {
      await what();
    } catch (e) {
      notify(errorMessage(e), 'error');
    } finally {
      busy = '';
    }
  }
</script>

{#snippet row(a: ChangeAsk, showChange: boolean)}
  {@const own = isMine(a, subject)}
  {@const holder = holderLabel(a, subject)}
  <li class:own class:held={heldByOther(a, subject)}>
    <button type="button" class="task" disabled={!!busy} title="Work on this task: its change becomes the working change" onclick={() => void run(a, () => workOnTask(a))}>
      <span class="line1">
        <span class="kind">{askKind(a)}</span>
        <span class="title">{a.title || a.action || a.id}</span>
      </span>
      <span class="line2">
        {#if showChange}<span class="change"><Icon name="diff" size={11} />{a.changeTitle || a.changeId}</span>{' · '}{/if}{#if holder}held by {holder}{:else}for {addressee(a, subject)}{/if}{#if waitingFor(a.since)}{` · ${waitingFor(a.since)}`}{/if}{#if a.declined}{' · turned down'}{/if}
      </span>
    </button>
    {#if a.mayAccept && !a.declined}
      <button type="button" class="ghost small take" disabled={!!busy} title="Accept this task: it becomes yours, the others no longer answer it" onclick={() => void run(a, () => accept(a))}>Accept</button>
    {:else if own && a.assignee}
      <button type="button" class="ghost small take" disabled={!!busy} title="Give this task back: it is offered again to the others" onclick={() => void run(a, () => reject(a))}>Give back</button>
    {/if}
    {#if a.processId}
      <button type="button" class="ghost small icon" title="Open the task: who it is for, who holds it{a.mayAssign ? ', give it to someone' : ''}" aria-label="Open the task" onclick={() => openTask(a, true)}><Icon name="more" size={13} /></button>
    {/if}
  </li>
{/snippet}

{#snippet area(title: string, icon: IconName, n: number, body: Snippet, hint = '')}
  <section class="ui-card area" aria-label={title}>
    <h2 class="ui-card-head" title={hint || undefined}><Icon name={icon} size={14} /><span class="name">{title}</span><span class="count">{n}</span></h2>
    <div class="area-body">{@render body()}</div>
  </section>
{/snippet}

<div class="dashboard">
  <header>
    <div class="heading">
      <h1>Tasks</h1>
      <span class="sub">{projectLabel ? `What the changes of ${projectLabel} wait for from people` : 'What the open changes wait for from people'}</span>
    </div>
    {#if !tasks.loaded}<span class="hint">Reading the tasks…</span>{/if}
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" onclick={() => void reloadTasks()}><Icon name="refresh" size={14} /></button>
    {#if tabsState.tabs.length}
      <button type="button" class="small" title="Back to the editors open ({tabsState.tabs.length})" onclick={() => (layout.full = '')}><Icon name="file" size={13} />Editors</button>
    {/if}
  </header>
  {#if tasks.error}<div class="alert">{tasks.error}</div>{/if}

  <div class="areas">
    {#snippet mineBody()}
      {#if mine.length}
        <ul class="list">{#each mine as a (taskKey(a))}{@render row(a, true)}{/each}</ul>
      {:else if tasks.loaded}
        <p class="empty">You hold no task. Accept one of those available to you to make it yours.</p>
      {/if}
    {/snippet}
    {@render area('My tasks', 'user', mine.length, mineBody, 'Tasks you hold, or addressed to you alone')}

    {#snippet offeredBody()}
      {#if offered.length}
        <ul class="list">{#each offered as a (taskKey(a))}{@render row(a, true)}{/each}</ul>
      {:else if tasks.loaded}
        <p class="empty">No other task of the open changes is yours to answer.</p>
      {/if}
    {/snippet}
    {@render area('Available to you', 'bell', offered.length, offeredBody, 'Tasks you may answer that nobody holds')}

    {#if working.change}
      {@const wc = working.change}
      {#snippet currentBody()}
        {#if current.length}
          <ul class="list">{#each current as a (a.id)}{@render row(a, false)}{/each}</ul>
        {:else if tasks.loaded}
          <p class="empty">“{wc.title}” waits for no one.</p>
        {/if}
      {/snippet}
      {@render area(`This change: ${wc.title}`, 'diff', current.length, currentBody, 'The working change')}
    {/if}

    {#if opened.length}
      {#snippet openedBody()}
        {#each opened as g (g.id)}
          <h3 title={g.id}><Icon name="diff" size={12} />{g.title}</h3>
          <ul class="list">{#each g.asks as a (a.id)}{@render row(a, false)}{/each}</ul>
        {/each}
      {/snippet}
      {@render area('Open in an editor', 'file', count(opened), openedBody, 'The changes open in a tab, other than the working one')}
    {/if}

    {#snippet projectBody()}
      {#each ofProject as g (g.changeId)}
        <h3 title={g.changeId}><Icon name="diff" size={12} />{g.title}</h3>
        <ul class="list">{#each g.asks as a (a.id)}{@render row(a, false)}{/each}</ul>
      {:else}
        {#if tasks.loaded}<p class="empty">No open change of the project waits on a person.</p>{/if}
      {/each}
    {/snippet}
    {@render area(projectLabel ? `Project: ${projectLabel}` : 'Project', 'folder', count(ofProject), projectBody, 'Every open change of the project waiting on a person')}
  </div>
</div>

<style>
  .dashboard {
    max-width: 1600px;
    margin: 0 auto;
    padding: var(--space-4) var(--space-5) var(--space-5);
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: var(--space-4);
  }
  .heading {
    flex: 1;
    min-width: 0;
  }
  h1 {
    margin: 0;
    font-size: 1.25rem;
  }
  .sub {
    color: var(--text-3);
    font-size: var(--fs-sm);
  }
  .areas {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 360px), 1fr));
    gap: var(--space-4);
    align-items: start;
  }
  .area {
    margin: 0;
    display: flex;
    flex-direction: column;
    max-height: 60vh;
  }
  h2.ui-card-head {
    margin: 0;
    flex: none;
  }
  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .area-body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding-bottom: 0.3rem;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    margin: 0.5rem 0 0.1rem;
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
    font-weight: 400;
    line-height: 16px;
    text-align: center;
  }
  .empty {
    margin: 0;
    padding: 0.6rem 0.8rem;
    font-size: 0.88em;
    color: var(--muted);
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
  .list li:last-child {
    border-bottom: none;
  }
  .list li.own {
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .list li.held .title {
    color: var(--text-2);
  }
  .icon {
    flex: none;
    padding: 0.1rem 0.2rem;
  }
  .task {
    flex: 1;
    min-width: 0;
    display: grid;
    gap: 1px;
    padding: 0.4rem 0.4rem 0.4rem 0.8rem;
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
