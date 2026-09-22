<script lang="ts">
  import { engine, errorMessage, formatDate, shortId, type Process } from '../api';
  import { href } from '../nav.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import WorldState from './WorldState.svelte';
  import StepsTimeline from './StepsTimeline.svelte';
  import HumanTaskForm from './HumanTaskForm.svelte';

  let { id, onupdate }: { id: string; onupdate?: (p: Process) => void } = $props();

  const POLL_MS = 1500;

  let process = $state<Process | undefined>();
  let error = $state('');
  let answer = $state('');
  let answering = $state(false);

  function set(p: Process | undefined) {
    if (!p) return;
    process = p;
    onupdate?.(p);
  }

  async function refresh(signal?: AbortSignal) {
    try {
      set((await engine.getProcess(id, signal)).process);
      error = '';
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    }
  }

  // Chargement initial à chaque changement d'identifiant.
  $effect(() => {
    void id;
    process = undefined;
    error = '';
    const ctrl = new AbortController();
    refresh(ctrl.signal);
    return () => ctrl.abort();
  });

  // Rafraîchissement périodique tant que le processus tourne.
  const status = $derived(process?.status ?? '');
  $effect(() => {
    if (status !== 'running') return;
    const ctrl = new AbortController();
    const timer = setInterval(() => refresh(ctrl.signal), POLL_MS);
    return () => {
      clearInterval(timer);
      ctrl.abort();
    };
  });

  async function sendAnswer(e: SubmitEvent) {
    e.preventDefault();
    if (!answer.trim()) return;
    answering = true;
    error = '';
    try {
      set((await engine.answerIntent(id, answer.trim())).process);
      answer = '';
    } catch (err) {
      error = errorMessage(err);
    } finally {
      answering = false;
    }
  }

  const ROLES: Record<string, string> = { user: 'Vous', human: 'Vous', assistant: 'Moteur', system: 'Moteur' };
</script>

{#if error}<div class="alert">{error}</div>{/if}

{#if !process}
  {#if !error}<p class="empty">Chargement…</p>{/if}
{:else}
  <section class="card">
    <div class="row">
      <h3 class="grow" style="margin: 0">
        Processus <code>{shortId(process.id)}</code>
      </h3>
      <StatusBadge status={process.status} />
      <button class="small" onclick={() => refresh()}>Actualiser</button>
    </div>
    <dl class="meta">
      <dt>Méthodologie</dt><dd>{process.methodology}</dd>
      <dt>Objectif</dt><dd>{#if process.goal}<code>{process.goal}</code>{:else}<span class="empty">à déterminer</span>{/if}</dd>
      {#if process.changeId}
        <dt>Changement</dt>
        <dd><a href={href('changement', process.changeId)}>{shortId(process.changeId)} →</a></dd>
      {/if}
      {#if process.updatedAt}<dt>Mis à jour</dt><dd>{formatDate(process.updatedAt)}</dd>{/if}
    </dl>
    {#if process.error}<pre class="perror">{process.error}</pre>{/if}
  </section>

  {#if process.status === 'clarifying' || process.turns?.length}
    <section class="card">
      <h3>Dialogue</h3>
      {#if process.turns?.length}
        <ol class="turns">
          {#each process.turns as t, i (i)}
            <li class:me={t.role === 'user' || t.role === 'human'}>
              <span class="who">{ROLES[t.role ?? ''] ?? t.role}</span>
              <div class="bubble">{t.text}</div>
            </li>
          {/each}
        </ol>
      {/if}
      {#if process.status === 'clarifying'}
        {#if process.question}<p class="question">{process.question}</p>{/if}
        {#if process.candidates?.length}
          <ul class="candidates">
            {#each process.candidates as c (c.goal)}
              <li>
                <code>{c.goal}</code>
                <span class="conf">{Math.round((c.confidence ?? 0) * 100)} %</span>
                {#if c.reason}<span class="hint">— {c.reason}</span>{/if}
              </li>
            {/each}
          </ul>
        {/if}
        <form class="row" onsubmit={sendAnswer}>
          <input class="grow" type="text" bind:value={answer} placeholder="Votre réponse…" aria-label="Réponse" />
          <button class="primary" type="submit" disabled={answering || !answer.trim()}>
            {answering ? 'Envoi…' : 'Répondre'}
          </button>
        </form>
      {/if}
    </section>
  {/if}

  {#if process.status === 'waiting' && process.pending}
    {#key `${process.id}:${process.pending.step}:${process.pending.action}`}
      <HumanTaskForm {process} onsubmitted={set} />
    {/key}
  {/if}

  <section class="card">
    <h3>Plan</h3>
    {#if process.plan?.length}
      <ol class="chips">
        {#each process.plan as a, i (i)}
          <li class="chip"><span class="n">{i + 1}</span>{a}</li>
        {/each}
      </ol>
    {:else}
      <p class="empty">Aucun plan.</p>
    {/if}
    {#if process.disabled?.length}
      <h4 style="margin-top: 0.9rem">Actions désactivées</h4>
      <ul class="chips">
        {#each process.disabled as a (a)}
          <li class="chip disabled">{a}</li>
        {/each}
      </ul>
    {/if}
  </section>

  <div class="two">
    <section class="card">
      <h3>État du monde</h3>
      <WorldState world={process.world} unknown={process.unknown} />
    </section>
    <section class="card">
      <h3>Étapes</h3>
      <StepsTimeline steps={process.steps} />
    </section>
  </div>
{/if}

<style>
  .meta {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.2rem 1rem;
    margin: 0.75rem 0 0;
    font-size: 0.9rem;
  }
  .meta dt {
    color: var(--muted);
  }
  .meta dd {
    margin: 0;
  }
  .perror {
    margin-top: 0.75rem;
    color: var(--danger);
    background: var(--danger-soft);
    border-color: transparent;
  }
  .turns {
    list-style: none;
    margin: 0 0 0.75rem;
    padding: 0;
    display: grid;
    gap: 0.5rem;
  }
  .turns li {
    display: grid;
    gap: 0.15rem;
    justify-items: start;
  }
  .turns li.me {
    justify-items: end;
  }
  .who {
    font-size: 0.75rem;
    color: var(--muted);
  }
  .bubble {
    max-width: 80%;
    padding: 0.45rem 0.75rem;
    border-radius: 12px;
    background: var(--surface-2);
    white-space: pre-wrap;
  }
  .me .bubble {
    background: var(--accent-soft);
  }
  .question {
    font-weight: 600;
  }
  .candidates {
    margin: 0 0 0.75rem;
    padding-left: 1.2rem;
    font-size: 0.9rem;
  }
  .conf {
    font-weight: 600;
    margin-left: 0.35rem;
  }
  .chip.disabled {
    text-decoration: line-through;
    color: var(--muted);
  }
  .two {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr);
    gap: 1rem;
  }
  @media (max-width: 1000px) {
    .two {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
