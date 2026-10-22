<script lang="ts">
  // The screen of a task (ADR 0158): one thing a change waits for from a person, its change, whom it is addressed to and
  // who holds it. The person accepts it (it becomes theirs, the others no longer answer it), gives it back or turns it
  // down, answers it in place; an administrator of the project of the change, or of a project above it, gives it to a
  // person who may answer it, or takes it back. The engine says what the caller may do (mayAnswer, mayAccept,
  // mayAssign) and refuses the rest; the task is read again when its run moves.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import PendingTask from '../../components/PendingTask.svelte';
  import { engine, errorMessage, formatDate, nodeTitle, shortId, type ChangeAsk } from '../../api';
  import { processes, ingestProcess } from '../../stores/live.svelte';
  import { changes } from '../../stores/catalog.svelte';
  import { me, ns, subjectOfKey, types } from '../../stores/session.svelte';
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import { headGraph } from '../../graphEdit';
  import { accept, assign, reject, tasks, workOnTask } from '../../stores/tasks.svelte';
  import { holderLabel, isMine } from '../../tasks';
  import { addressee, answeredInPlace, askKind, waitingFor } from '../../changeAsks';

  let { tab }: { tab: Tab } = $props();
  const changeId = $derived(tab.params.change ?? '');
  const id = $derived(tab.params.id ?? '');

  let ask = $state<ChangeAsk | undefined>();
  let loaded = $state(false);
  let error = $state('');

  // the run of the task moves, or the change does: read the task again
  const version = $derived(
    [...processes.values()]
      .filter((p) => p.changeId === changeId)
      .map((p) => `${p.id}:${p.status}:${p.updatedAt}`)
      .join('|'),
  );
  $effect(() => {
    void version;
    void stamp(keyOf.change(changeId));
    const ctl = new AbortController();
    engine
      .getChangeAsks(changeId, ctl.signal)
      .then((r) => {
        ask = (r.asks ?? []).find((a) => a.id === id);
        error = '';
        loaded = true;
      })
      .catch((e) => {
        if (ctl.signal.aborted) return;
        error = errorMessage(e);
        loaded = true;
      });
    return () => ctl.abort();
  });

  const subject = $derived(me());
  const changeTitle = $derived(
    changes.items.find((c) => c.id === changeId)?.title || tasks.asks.find((a) => a.changeId === changeId)?.changeTitle || shortId(changeId),
  );
  const run = $derived(ask?.processId ? processes.get(ask.processId) : undefined);
  const own = $derived(!!ask && isMine(ask, subject));
  const holder = $derived(ask ? holderLabel(ask, subject) : '');
  /** how the holder came to hold it */
  const heldNote = $derived.by(() => {
    if (!ask?.assignee) return '';
    const on = ask.assignedAt ? ` on ${formatDate(ask.assignedAt)}` : '';
    if (ask.assignedBy && ask.assignedBy !== ask.assignee) return `given by ${ask.assignedBy === subject ? 'you' : ask.assignedBy}${on}`;
    return ask.assignedAt ? `accepted${on}` : '';
  });
  /** the change of the task, for its asks */
  const target = $derived<ChangeAsk | undefined>(ask ? { ...ask, changeId, changeTitle } : undefined);

  let busy = $state(false);
  let reason = $state('');
  let turningDown = $state(false);
  async function act(what: () => Promise<unknown>, done = '') {
    busy = true;
    try {
      await what();
      if (done) notify(done, 'ok');
    } catch (e) {
      notify(errorMessage(e), 'error');
    } finally {
      busy = false;
    }
  }

  // the people the task may be given to: the users of the organisation the engine says may answer it
  interface Candidate {
    subject: string;
    label: string;
  }
  let candidates = $state<Candidate[] | undefined>();
  let candidatesError = $state('');
  let pick = $state('');
  $effect(() => {
    const pid = ask?.processId;
    if (!ask?.mayAssign || !pid) return;
    let gone = false;
    candidatesError = '';
    void (async () => {
      try {
        const head = await headGraph(ns.organisation);
        const users = head.nodes
          .filter((n) => n.namespace === ns.organisation && n.type === types.user)
          .map((n) => ({ subject: (typeof n.props?.['subject'] === 'string' ? (n.props['subject'] as string) : '') || subjectOfKey(n.key ?? ''), label: nodeTitle(n) }))
          .filter((u) => u.subject);
        const r = await engine.listTaskCandidates(pid, users.map((u) => u.subject).slice(0, 500));
        const ok = new Set(r.subjects ?? []);
        if (!gone) candidates = users.filter((u) => ok.has(u.subject)).sort((a, b) => a.subject.localeCompare(b.subject));
      } catch (e) {
        if (!gone) candidatesError = errorMessage(e);
      }
    })();
    return () => (gone = true);
  });
  const others = $derived((candidates ?? []).filter((c) => c.subject !== ask?.assignee));
  const nameOf = (s: string): string => {
    const c = candidates?.find((x) => x.subject === s);
    return c && c.label && c.label !== s ? `${c.label} (${s})` : s;
  };
</script>

<div class="task-tab">
  {#if !loaded}
    <p class="hint">Reading the task…</p>
  {:else if error}
    <div class="alert">{error}</div>
  {:else if !ask || !target}
    <div class="gone">
      <Icon name="check" size={18} />
      <div>
        <strong>This task no longer waits</strong>
        <p class="hint">It was answered, or its run moved on.</p>
        <button type="button" class="small" onclick={() => openTab({ kind: 'change', params: { id: changeId } }, { pin: true })}>Open the change</button>
      </div>
    </div>
  {:else}
    <header>
      <span class="kind">{askKind(ask)}</span>
      <h2>{ask.title || ask.action}</h2>
    </header>
    <p class="change">
      <Icon name="diff" size={13} /> {changeTitle}
      <button type="button" class="link small" disabled={busy} onclick={() => void act(() => workOnTask(target))}>work on it in the change</button>
    </p>

    <dl class="facts">
      <dt>For</dt>
      <dd>{addressee(ask, subject)}</dd>
      <dt>Held by</dt>
      <dd>
        {#if holder}
          <strong>{holder === 'you' ? 'you' : nameOf(holder)}</strong>
          {#if heldNote}<span class="hint">{heldNote}</span>{/if}
        {:else}
          <span class="hint">nobody: anyone it is for may take it</span>
        {/if}
      </dd>
      {#if ask.step}<dt>Step</dt><dd><code>{ask.step}</code></dd>{/if}
      {#if ask.action}<dt>Action</dt><dd><code>{ask.action}</code></dd>{/if}
      <dt>Waiting</dt>
      <dd>{waitingFor(ask.since) || '—'}{#if ask.since} <span class="hint">since {formatDate(ask.since)}</span>{/if}</dd>
      {#if ask.processId}
        <dt>Run</dt>
        <dd><button type="button" class="link small" onclick={() => openTab({ kind: 'run', params: { id: ask?.processId ?? '' } }, { pin: true })}>{run?.agent || shortId(ask.processId)}</button></dd>
      {/if}
    </dl>
    {#if ask.detail}<p class="detail">{ask.detail}</p>{/if}

    <section aria-label="Your part">
      <h3>You</h3>
      {#if own}
        <p>This task is yours{ask.assignee ? '' : ': it is addressed to you alone'}.</p>
      {:else if holder}
        <p>{nameOf(holder)} holds this task: only they answer it{ask.mayAssign ? ', unless you give it to someone else' : ''}.</p>
      {:else if ask.declined}
        <p>You turned this task down: it is no longer offered to you. You may still accept it.</p>
      {:else if ask.mayAnswer}
        <p>This task is available to you: accept it to make it yours, the others will no longer answer it.</p>
      {:else}
        <p>You may not answer this task.</p>
      {/if}
      <div class="row">
        {#if ask.mayAccept}
          <button type="button" class="primary small" disabled={busy} onclick={() => void act(() => accept(target), 'Task accepted: it is yours.')}>Accept</button>
        {/if}
        {#if (own && ask.assignee) || (ask.mayAnswer && !ask.assignee && !ask.declined && ask.processId)}
          <button type="button" class="small" disabled={busy} onclick={() => (turningDown = !turningDown)}>{own && ask.assignee ? 'Give back…' : 'Turn down…'}</button>
        {/if}
      </div>
      {#if turningDown}
        <form
          class="reason"
          onsubmit={(e) => {
            e.preventDefault();
            void act(async () => {
              await reject(target, reason.trim());
              turningDown = false;
              reason = '';
            }, own ? 'Task given back.' : 'Task turned down.');
          }}
        >
          <textarea rows="2" placeholder="Why (optional, kept in the journal of the change)" bind:value={reason}></textarea>
          <div class="row">
            <button type="submit" class="small" disabled={busy}>{own && ask.assignee ? 'Give back' : 'Turn down'}</button>
            <button type="button" class="ghost small" onclick={() => (turningDown = false)}>Cancel</button>
          </div>
        </form>
      {/if}
    </section>

    {#if ask.mayAssign}
      <section aria-label="Assignment">
        <h3>Assignment <span class="hint">as an administrator of the project</span></h3>
        {#if candidatesError}
          <div class="alert small">{candidatesError}</div>
        {:else if !candidates}
          <p class="hint">Reading who may answer it…</p>
        {:else}
          <div class="row">
            <select bind:value={pick} aria-label="Give the task to" disabled={busy || !others.length}>
              <option value="">{others.length ? 'Give it to…' : 'Nobody else may answer it'}</option>
              {#each others as c (c.subject)}<option value={c.subject}>{c.label && c.label !== c.subject ? `${c.label} (${c.subject})` : c.subject}</option>{/each}
            </select>
            <button type="button" class="small" disabled={busy || !pick} onclick={() => void act(async () => {
                await assign(target, pick);
                pick = '';
              }, 'Task given.')}>Assign</button>
            {#if ask.assignee}
              <button type="button" class="ghost small" disabled={busy} title="Nobody holds it any more: it is offered again to everyone it is for" onclick={() => void act(() => assign(target, ''), 'Task taken back.')}>Take back</button>
            {/if}
          </div>
          <p class="hint">Only the people who may answer the task are offered ({candidates.length}).</p>
        {/if}
      </section>
    {/if}

    {#if ask.mayAnswer && answeredInPlace(ask) && run?.pending}
      <section aria-label="Answer">
        <h3>Answer</h3>
        <PendingTask process={run} ondecided={ingestProcess} onopen={(pid) => openTab({ kind: 'run', params: { id: pid } }, { pin: true })} />
      </section>
    {/if}
  {/if}
</div>

<style>
  .task-tab {
    padding: 1rem 1.2rem 2rem;
    max-width: 860px;
  }
  header {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
  }
  h2 {
    margin: 0;
    font-size: 1.25rem;
  }
  h3 {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    margin: 1.2rem 0 0.4rem;
    font-size: 1rem;
  }
  .kind {
    flex: none;
    font-size: var(--fs-xs);
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: var(--accent);
  }
  .change {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    color: var(--text-2);
    margin: 0.3rem 0 0.8rem;
  }
  .facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.25rem 1rem;
    margin: 0;
  }
  .facts dt {
    color: var(--muted);
  }
  .facts dd {
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: baseline;
  }
  .detail {
    white-space: pre-wrap;
    margin: 0.8rem 0 0;
  }
  section {
    border-top: 1px solid var(--border);
    margin-top: 1rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
  }
  .reason {
    display: grid;
    gap: 0.4rem;
    margin-top: 0.5rem;
    max-width: 520px;
  }
  .gone {
    display: flex;
    gap: 0.8rem;
    align-items: flex-start;
  }
  .gone p {
    margin: 0.2rem 0 0.6rem;
  }
</style>
