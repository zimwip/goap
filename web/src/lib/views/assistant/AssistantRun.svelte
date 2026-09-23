<script lang="ts">
  // Suivi d'une exécution dans la conversation de l'assistant, en langage
  // courant : agent choisi, progression, questions, tâches, résultat.
  import { untrack } from 'svelte';
  import AssistantRun from './AssistantRun.svelte';
  import HumanTaskForm from '../../components/HumanTaskForm.svelte';
  import ApprovalPanel from '../../components/ApprovalPanel.svelte';
  import { engine, graph, errorMessage, int, formatInt, type ChangeSet, type LogLine, type Process } from '../../api';
  import { watchEvents, type StreamStatus } from '../../stream';
  import { processes, ingestProcess, ingestEvent } from '../../stores/live.svelte';
  import { loadMethodology, agentLabel, actionLabel, goalLabel } from '../../stores/assistant.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { renderMarkdown } from '../../markdown';

  let {
    processId,
    live = false,
    depth = 0,
  }: {
    processId: string;
    /** suivi en direct (flux WatchEvents du processus et de ses sous-agents) */
    live?: boolean;
    depth?: number;
  } = $props();

  const p = $derived<Process | undefined>(processes.get(processId));
  let error = $state('');
  let answer = $state('');
  let answering = $state(false);
  let stream = $state<StreamStatus>('connecting');
  let liveLogs = $state<LogLine[]>([]);
  let showLogs = $state(false);

  async function refresh() {
    try {
      ingestProcess((await engine.getProcess(processId)).process);
      error = '';
    } catch (e) {
      error = errorMessage(e);
    }
  }

  // État à jour à l'affichage (l'historique peut dater).
  $effect(() => {
    void processId;
    untrack(() => void refresh());
  });

  $effect(() => {
    if (!live) return;
    const pid = processId;
    const stop = watchEvents({
      processId: pid,
      onEvent: (e) => {
        if (e.log && (e.log.processId ?? pid) === pid) liveLogs = [...liveLogs, e.log].slice(-200);
        ingestEvent(e, false);
      },
      onStatus: (s) => (stream = s),
    });
    return stop;
  });

  const status = $derived(p?.status ?? '');
  const done = $derived(status === 'completed' || status === 'failed');

  // Repli si le flux échoue.
  $effect(() => {
    if (!live || done || (stream !== 'retrying' && stream !== 'stopped')) return;
    const t = setInterval(() => void refresh(), 2000);
    return () => clearInterval(t);
  });

  $effect(() => {
    if (p?.methodology) void loadMethodology(p.methodology);
  });

  // Résultat : items du changement à la fin.
  let change = $state<ChangeSet | undefined>();
  $effect(() => {
    if (status !== 'completed' || !p?.changeId || depth > 0) return;
    const id = p.changeId;
    untrack(() => {
      graph
        .getChange(id)
        .then((r) => (change = r.change))
        .catch(() => undefined);
    });
  });

  const children = $derived.by(() => {
    const ids = new Set<string>();
    for (const s of p?.steps ?? []) for (const c of s.childProcessIds ?? []) ids.add(c);
    if (p?.pending?.childProcessId) ids.add(p.pending.childProcessId);
    return [...ids];
  });

  const logs = $derived.by(() => {
    const out: LogLine[] = [];
    for (const s of p?.steps ?? []) for (const l of s.logs ?? []) out.push({ ...l, action: l.action || s.action });
    const seen = new Set(out.map((l) => `${l.time}|${l.message}`));
    for (const l of liveLogs) if (!seen.has(`${l.time}|${l.message}`)) out.push(l);
    return out;
  });

  const extraTurns = $derived((p?.turns ?? []).slice(1));
  const agentName = $derived(agentLabel(p?.methodology, p?.agent));

  async function reply(text: string) {
    if (!text.trim() || !p?.id) return;
    answering = true;
    error = '';
    try {
      ingestProcess((await engine.answerIntent(p.id, text.trim())).process);
      answer = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      answering = false;
    }
  }

  function candidateLabel(c: { goal?: string; agent?: string; methodology?: string }): string {
    const g = c.goal ? goalLabel(c.methodology, c.goal) : '';
    const a = c.agent ? agentLabel(c.methodology, c.agent) : '';
    return [a, g].filter(Boolean).join(' — ') || c.goal || c.agent || '?';
  }

  function markdownOf(data: Record<string, unknown> | undefined): { md?: string; text?: string } {
    const md = data?.['markdown'];
    const text = data?.['text'];
    return { md: typeof md === 'string' ? md : undefined, text: typeof text === 'string' ? text : undefined };
  }

  const artifacts = $derived((change?.items ?? []).filter((i) => i.kind === 'artifact'));
  const counts = $derived({
    impacts: (change?.items ?? []).filter((i) => i.kind === 'impact').length,
    proposals: (change?.items ?? []).filter((i) => i.kind === 'proposal').length,
    accepted: (change?.items ?? []).filter((i) => i.kind === 'proposal' && i.status === 'accepted').length,
  });
  const tokens = $derived(int(p?.usage?.inputTokens) + int(p?.usage?.outputTokens));
</script>

<div class="run" class:nested={depth > 0}>
  {#if !p}
    {#if error}<div class="bubble bot err">Je n'arrive pas à suivre cette demande : {error}</div>{:else}<div class="bubble bot">…</div>{/if}
  {:else}
    {#if depth > 0}
      <div class="deleg">↳ délègue à <strong>« {agentName} »</strong></div>
    {:else if p.agent}
      <div class="bubble bot">
        L'agent <strong>« {agentName} »</strong> s'en occupe{#if p.goal}&nbsp;: {goalLabel(p.methodology, p.goal).replace(/\.$/, '')}{/if}.
        {#if p.trigger}<span class="muted">(déclenché par {p.trigger})</span>{/if}
      </div>
    {/if}

    {#each extraTurns as t, i (i)}
      <div class="bubble" class:me={t.role === 'user' || t.role === 'human'} class:bot={!(t.role === 'user' || t.role === 'human')}>{t.text}</div>
    {/each}

    {#if status === 'clarifying'}
      <div class="bubble bot">{p.question || 'Pouvez-vous préciser votre demande ?'}</div>
      {#if p.candidates?.length}
        <div class="choices">
          {#each p.candidates as c, i (i)}
            <button type="button" class="choice" disabled={answering} onclick={() => reply(c.goal || c.agent || '')}>
              {candidateLabel(c)}
            </button>
          {/each}
        </div>
      {/if}
      <form
        class="answer"
        onsubmit={(e) => {
          e.preventDefault();
          void reply(answer);
        }}
      >
        <input type="text" bind:value={answer} placeholder="Votre réponse…" aria-label="Votre réponse" data-no-pin />
        <button type="submit" class="primary small" disabled={answering || !answer.trim()}>Répondre</button>
      </form>
    {/if}

    {#if p.steps?.length}
      <ol class="steps">
        {#each p.steps as s, i (s.index ?? i)}
          {@const st = s.error ? 'err' : !s.endedAt ? 'going' : s.effectsMet ? 'ok' : 'part'}
          <li class={st}>
            <span class="mark" aria-hidden="true">{st === 'ok' ? '✓' : st === 'err' ? '✗' : st === 'going' ? '' : '•'}</span>
            <span>{actionLabel(p.methodology, s.action)}</span>
            {#if st === 'err'}<span class="errtxt">— {s.error}</span>{/if}
          </li>
        {/each}
      </ol>
    {/if}

    {#if logs.length}
      <button type="button" class="link small-link" onclick={() => (showLogs = !showLogs)}>
        {showLogs ? 'Masquer le journal' : `Voir le journal (${logs.length})`}
      </button>
      {#if showLogs}
        <ol class="logs">
          {#each logs as l, i (i)}<li class="lvl-{l.level || 'info'}">{l.message}</li>{/each}
        </ol>
      {:else if status === 'running'}
        <div class="lastlog">{logs[logs.length - 1].message}</div>
      {/if}
    {/if}

    {#each children as c (c)}
      {#if depth < 3}<AssistantRun processId={c} depth={depth + 1} />{/if}
    {/each}

    {#if status === 'running'}
      <div class="working"><span class="spin" aria-hidden="true"></span> Je travaille…{#if p.plan?.length} prochaine étape : {actionLabel(p.methodology, p.plan[0]).toLowerCase()}{/if}</div>
    {:else if status === 'waiting' && p.pending}
      {#key `${p.id}:${p.pending.step}:${p.pending.action}:${p.pending.kind}`}
        {#if p.pending.kind === 'input'}
          <div class="bubble bot">J'ai besoin de vous pour continuer : {p.pending.description || actionLabel(p.methodology, p.pending.action)}.</div>
          <div class="inline"><HumanTaskForm process={p} onsubmitted={ingestProcess} /></div>
        {:else if p.pending.kind === 'approval'}
          <div class="bubble bot">Cette étape doit être approuvée par une personne habilitée.</div>
          <div class="inline"><ApprovalPanel process={p} ondecided={ingestProcess} /></div>
        {:else if p.pending.kind === 'agent'}
          <div class="working"><span class="spin" aria-hidden="true"></span> En attente d'un autre agent…</div>
        {/if}
      {/key}
    {:else if status === 'stuck'}
      <div class="bubble bot warn">Je suis bloqué : aucune action ne permet d'atteindre l'objectif avec les informations disponibles.{#if p.error} ({p.error}){/if}</div>
    {:else if status === 'failed'}
      <div class="bubble bot err">La demande a échoué{#if p.error}&nbsp;: {p.error}{/if}.</div>
    {:else if status === 'completed' && depth > 0}
      <div class="muted small">terminé</div>
    {:else if status === 'completed'}
      <div class="bubble bot result">
        <p><strong>C'est terminé.</strong>
          {#if counts.impacts || counts.proposals}
            {counts.impacts} élément{counts.impacts > 1 ? 's' : ''} impacté{counts.impacts > 1 ? 's' : ''},
            {counts.proposals} proposition{counts.proposals > 1 ? 's' : ''}{#if counts.accepted} ({counts.accepted} acceptée{counts.accepted > 1 ? 's' : ''}){/if}.
          {/if}
        </p>
        {#each artifacts as a (a.id)}
          {@const x = markdownOf(a.data)}
          <div class="artifact">
            {#if x.md !== undefined}
              <!-- Markdown échappé par renderMarkdown. -->
              <div class="md">{@html renderMarkdown(x.md)}</div>
            {:else if x.text !== undefined}
              <p class="text">{x.text}</p>
            {/if}
          </div>
        {/each}
        <p class="muted small">
          {#if tokens}{formatInt(tokens)} tokens · {p.usage?.llmCalls ?? 0} appel{(p.usage?.llmCalls ?? 0) > 1 ? 's' : ''} au modèle ·{/if}
          <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: p?.id ?? '' } }, { pin: true })}>Voir le détail</button>
        </p>
      </div>
    {/if}

    {#if error && p}<div class="muted small err">{error}</div>{/if}
    {#if depth === 0 && !done && status !== 'completed'}
      <button type="button" class="link small-link" onclick={() => openTab({ kind: 'run', params: { id: p?.id ?? '' } })}>Voir le détail</button>
    {/if}
  {/if}
</div>

<style>
  .run {
    display: grid;
    gap: 0.4rem;
    justify-items: start;
  }
  .nested {
    margin-left: 1rem;
    padding-left: 0.6rem;
    border-left: 2px solid var(--border);
  }
  .bubble {
    max-width: 92%;
    padding: 0.45rem 0.7rem;
    border-radius: 12px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .bubble.bot {
    background: var(--surface-2);
    border-top-left-radius: 4px;
  }
  .bubble.me {
    justify-self: end;
    background: var(--accent);
    color: var(--accent-text);
    border-top-right-radius: 4px;
  }
  .bubble.err {
    background: var(--danger-soft);
    color: var(--danger);
  }
  .bubble.warn {
    background: var(--warn-soft);
    color: var(--warn);
  }
  .result {
    white-space: normal;
    max-width: 100%;
  }
  .result p {
    margin: 0 0 0.4rem;
  }
  .deleg {
    color: var(--muted);
    font-size: 0.95em;
  }
  .choices {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
  }
  .choice {
    border-radius: 999px;
    font-weight: 500;
    background: var(--accent-soft);
    border-color: transparent;
    color: var(--accent);
  }
  .answer {
    display: flex;
    gap: 0.4rem;
    width: 100%;
  }
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.1rem;
  }
  .steps li {
    display: flex;
    gap: 0.4rem;
    align-items: baseline;
  }
  .mark {
    width: 1rem;
    text-align: center;
    flex: none;
  }
  .ok .mark {
    color: var(--ok);
  }
  .err .mark,
  .errtxt {
    color: var(--danger);
  }
  .steps li.going .mark::before {
    content: '';
    display: inline-block;
    width: 9px;
    height: 9px;
    border-radius: 50%;
    border: 2px solid var(--accent);
    border-right-color: transparent;
    animation: spin 0.8s linear infinite;
  }
  .working {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    color: var(--muted);
  }
  .spin {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    border: 2px solid var(--accent);
    border-right-color: transparent;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .logs {
    margin: 0;
    padding: 0.3rem 0.6rem 0.3rem 1.4rem;
    font-size: 0.9em;
    color: var(--muted);
    background: var(--surface-2);
    border-radius: var(--radius-sm);
    max-height: 12rem;
    overflow: auto;
    width: 100%;
  }
  .lastlog {
    font-size: 0.9em;
    color: var(--muted);
    font-style: italic;
  }
  .lvl-warn {
    color: var(--warn);
  }
  .lvl-error {
    color: var(--danger);
  }
  .small-link {
    font-size: 0.9em;
  }
  .muted {
    color: var(--muted);
  }
  .small {
    font-size: 0.9em;
  }
  .inline {
    width: 100%;
  }
  .artifact {
    border-top: 1px solid var(--border);
    padding-top: 0.4rem;
    margin-top: 0.3rem;
  }
  .md :global(h2),
  .md :global(h3) {
    font-size: 1rem;
    margin: 0.4rem 0 0.3rem;
  }
  .md :global(table) {
    font-size: 0.92em;
  }
  .text {
    white-space: pre-wrap;
  }
</style>
