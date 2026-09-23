<script lang="ts">
  // Visionneuse d'exécution en direct (WatchEvents du processus ; repli sur
  // GetProcess toutes les 2 s si le flux échoue).
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import WorldState from '../../components/WorldState.svelte';
  import StepsTimeline from '../../components/StepsTimeline.svelte';
  import HumanTaskForm from '../../components/HumanTaskForm.svelte';
  import ApprovalPanel from '../../components/ApprovalPanel.svelte';
  import IntentDialogue from './IntentDialogue.svelte';
  import { provideActions } from '../../shell/workbench.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import {
    engine,
    errorMessage,
    formatDate,
    formatInt,
    shortId,
    JAEGER_URL,
    type LogLine,
    type Process,
  } from '../../api';
  import { watchEvents, type StreamStatus } from '../../stream';
  import { processes, ingestProcess, ingestEvent, childrenOf } from '../../stores/live.svelte';

  let { tab }: { tab: Tab } = $props();

  const id = $derived(tab.params.id ?? '');
  const process = $derived<Process | undefined>(processes.get(id));
  let error = $state('');
  let loading = $state(false);
  let stream = $state<StreamStatus>('connecting');
  let liveLogs = $state<LogLine[]>([]);

  const POLL_MS = 2000;
  /**
   * Le serveur n'envoie les en-têtes du flux qu'avec le premier événement :
   * « connecting » est l'état normal d'un flux inactif. On n'y fait qu'une
   * vérification lente ; l'interrogation à 2 s ne sert qu'en cas d'échec.
   */
  const IDLE_POLL_MS = 10_000;
  const TERMINAL = ['completed', 'failed'];

  function set(p: Process | undefined) {
    if (p) ingestProcess(p);
  }

  async function refresh(signal?: AbortSignal) {
    loading = true;
    try {
      set((await engine.getProcess(id, signal)).process);
      error = '';
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  // Chargement initial et flux du processus.
  $effect(() => {
    const pid = id;
    const ctrl = new AbortController();
    void refresh(ctrl.signal);
    const stop = watchEvents({
      processId: pid,
      onEvent: (e) => {
        if (e.log && (e.log.processId ?? pid) === pid) liveLogs = [...liveLogs, e.log].slice(-500);
        // Le flux global enregistre déjà l'événement : mise à jour des données seulement.
        ingestEvent(e, false);
      },
      onStatus: (s) => (stream = s),
    });
    return () => {
      ctrl.abort();
      stop();
    };
  });

  const status = $derived(process?.status ?? '');

  // Repli : interrogation périodique quand le flux a échoué.
  $effect(() => {
    if (stream === 'open' || TERMINAL.includes(status) || !id) return;
    const ctrl = new AbortController();
    const timer = setInterval(() => void refresh(ctrl.signal), stream === 'connecting' ? IDLE_POLL_MS : POLL_MS);
    return () => {
      clearInterval(timer);
      ctrl.abort();
    };
  });

  const children = $derived(childrenOf(process));
  const traceUrl = $derived(process?.traceId ? `${JAEGER_URL}/trace/${process.traceId}` : '');

  function openRun(pid: string, pin = false) {
    openTab({ kind: 'run', params: { id: pid } }, { pin });
  }

  provideActions(
    () => tab.id,
    () => [
      { id: 'refresh', label: 'Actualiser', icon: 'refresh', disabled: loading, run: () => refresh() },
      {
        id: 'trace',
        label: 'Trace',
        icon: 'trace',
        disabled: !traceUrl,
        title: traceUrl ? 'Ouvrir la trace dans Jaeger' : 'Pas de trace pour ce processus',
        run: () => window.open(traceUrl, '_blank', 'noopener'),
      },
      {
        id: 'change',
        label: 'Changement',
        icon: 'diff',
        disabled: !process?.changeId,
        run: () => openTab({ kind: 'change', params: { id: process?.changeId ?? '' } }),
      },
      {
        id: 'parent',
        label: 'Processus parent',
        icon: 'runs',
        disabled: !process?.parentId,
        run: () => openRun(process?.parentId ?? ''),
      },
    ],
  );
</script>

<div class="editor-page wide">
  {#if error}<div class="alert">{error}</div>{/if}
  {#if !process}
    {#if !error}<p class="empty">Chargement…</p>{/if}
  {:else}
    <div class="editor-head">
      <Icon name={process.parentId ? 'bot' : 'runs'} size={18} />
      <h2>{process.title || `Exécution ${shortId(process.id)}`}</h2>
      <StatusBadge status={process.status} />
      {#if process.trigger}<span class="trig" title="Lancé automatiquement par un déclencheur">déclenché par {process.trigger}</span>{/if}
      <span
        class="live {stream}"
        title={stream === 'retrying' || stream === 'stopped'
          ? 'Flux indisponible : interrogation toutes les 2 s'
          : 'Mises à jour en direct (WatchEvents)'}
      >
        <span class="led" aria-hidden="true"></span>{stream === 'retrying' || stream === 'stopped' ? 'interrogation' : 'en direct'}
      </span>
      <span class="grow"></span>
      {#if traceUrl}
        <a class="trace" href={traceUrl} target="_blank" rel="noreferrer"><Icon name="external" size={13} /> Trace</a>
      {/if}
    </div>

    <div class="top">
      <section class="card">
        <dl class="meta">
          <dt>Identifiant</dt><dd><code>{process.id}</code></dd>
          <dt>Méthodologie</dt><dd>{process.methodology || '—'}</dd>
          <dt>Agent</dt><dd>{#if process.agent}<code>{process.agent}</code>{:else}<span class="empty">à déterminer</span>{/if}</dd>
          <dt>Planificateur</dt><dd>{process.planner || '—'}</dd>
          <dt>Objectif</dt><dd>{#if process.goal}<code>{process.goal}</code>{:else}<span class="empty">à déterminer</span>{/if}</dd>
          {#if process.trigger}
            <dt>Déclenché par</dt><dd><span class="trig"><Icon name="zap" size={12} /> {process.trigger}</span></dd>
          {/if}
          {#if process.initiator?.subject}
            <dt>Initiateur</dt>
            <dd>
              {process.initiator.subject}{#if process.initiator.roles?.length}
                <span class="hint"> ({process.initiator.roles.join(', ')})</span>{/if}
            </dd>
          {/if}
          {#if process.changeId}
            <dt>Changement</dt>
            <dd><button type="button" class="link mono" onclick={() => openTab({ kind: 'change', params: { id: process.changeId ?? '' } })}>{shortId(process.changeId)}</button></dd>
          {/if}
          {#if process.baselineId}
            <dt>Référentiel</dt>
            <dd><button type="button" class="link mono" onclick={() => openTab({ kind: 'baseline', params: { id: process.baselineId ?? '' } })}>{shortId(process.baselineId)}</button></dd>
          {/if}
          {#if process.parentId}
            <dt>Parent</dt>
            <dd><button type="button" class="link mono" onclick={() => openRun(process.parentId ?? '')}>{shortId(process.parentId)}</button></dd>
          {/if}
          {#if children.length}
            <dt>Sous-agents</dt>
            <dd class="kids">
              {#each children as c (c)}
                {@const cp = processes.get(c)}
                <button type="button" class="link" onclick={() => openRun(c)}>
                  {cp?.agent || shortId(c)}
                </button>
                {#if cp}<StatusBadge status={cp.status} />{/if}
              {/each}
            </dd>
          {/if}
          <dt>Créé</dt><dd>{formatDate(process.createdAt)}</dd>
          {#if process.updatedAt}<dt>Mis à jour</dt><dd>{formatDate(process.updatedAt)}</dd>{/if}
        </dl>
        {#if process.error}<pre class="perror">{process.error}</pre>{/if}
      </section>
      <section class="stats" aria-label="Totaux">
        <div class="stat"><span class="v">{formatInt(process.usage?.inputTokens)}</span><span class="k">tokens entrée</span></div>
        <div class="stat"><span class="v">{formatInt(process.usage?.outputTokens)}</span><span class="k">tokens sortie</span></div>
        <div class="stat"><span class="v">{process.usage?.llmCalls ?? 0}</span><span class="k">appels LLM</span></div>
        <div class="stat"><span class="v">{process.usage?.toolCalls ?? 0}</span><span class="k">appels d'outils</span></div>
      </section>
    </div>

    {#if process.status === 'clarifying' || process.turns?.length}
      <IntentDialogue {process} onupdate={set} />
    {/if}

    {#if process.status === 'waiting' && process.pending}
      {#key `${process.id}:${process.pending.step}:${process.pending.action}:${process.pending.kind}`}
        {#if process.pending.kind === 'approval'}
          <ApprovalPanel {process} ondecided={set} />
        {:else if process.pending.kind === 'agent'}
          <section class="card waiting-agent">
            <h3>En attente d'un sous-agent</h3>
            <p>
              L'action <code>{process.pending.action}</code> attend la fin du sous-agent
              {#if process.pending.childProcessId}
                <button type="button" class="link mono" onclick={() => openRun(process.pending?.childProcessId ?? '', true)}>
                  {processes.get(process.pending.childProcessId)?.agent || shortId(process.pending.childProcessId)}
                </button>
                {#if processes.get(process.pending.childProcessId)}
                  <StatusBadge status={processes.get(process.pending.childProcessId)?.status} />
                {/if}
              {/if}. Elle sera rejouée quand il se terminera.
            </p>
            {#if process.pending.description}<p class="hint">{process.pending.description}</p>{/if}
          </section>
        {:else}
          <HumanTaskForm {process} onsubmitted={set} />
        {/if}
      {/key}
    {/if}

    <section class="card">
      <h3>Plan</h3>
      {#if process.plan?.length}
        <ol class="chips">
          {#each process.plan as a, i (i)}
            {@const done = (process.steps ?? []).some((s) => s.action === a && s.effectsMet)}
            <li class="chip" class:done><span class="n">{i + 1}</span>{a}</li>
          {/each}
        </ol>
      {:else}
        <p class="empty">Aucun plan.</p>
      {/if}
      {#if process.disabled?.length}
        <h4 style="margin-top: 0.7rem">Actions désactivées</h4>
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
        <h3>Étapes <span class="hint">{process.steps?.length ?? 0}</span></h3>
        <StepsTimeline steps={process.steps} {liveLogs} onopenprocess={(c) => openRun(c)} />
      </section>
    </div>
  {/if}
</div>

<style>
  .wide {
    max-width: 1400px;
  }
  .live {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.85rem;
    color: var(--muted);
  }
  .led {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--warn);
  }
  .live.open .led,
  .live.connecting .led {
    background: var(--ok);
  }
  .trig {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.85rem;
    color: var(--info);
  }
  .trace {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-weight: 600;
  }
  .top {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 0.75rem;
    align-items: start;
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(2, minmax(110px, 1fr));
    gap: 0.5rem;
    margin-bottom: 0.75rem;
  }
  .stat {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.5rem 0.7rem;
    display: grid;
  }
  .stat .v {
    font-size: 1.25rem;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }
  .stat .k {
    font-size: 0.82rem;
    color: var(--muted);
  }
  .kids {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 0.5rem;
    align-items: center;
  }
  .perror {
    margin-top: 0.6rem;
    color: var(--danger);
    background: var(--danger-soft);
    border-color: transparent;
  }
  .waiting-agent {
    border-left: 3px solid var(--warn);
  }
  .chip.done {
    background: var(--ok-soft);
    color: var(--ok);
    border-color: transparent;
  }
  .chip.disabled {
    text-decoration: line-through;
    color: var(--muted);
  }
  .two {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.6fr);
    gap: 0.75rem;
  }
  @media (max-width: 1100px) {
    .two,
    .top {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
