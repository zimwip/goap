<script lang="ts">
  // Onglet « journal d'exécution » d'un changement (ADR 0011) : ticks
  // (observation + planification), exécutions d'actions, décisions humaines,
  // début / fin des processus, regroupés par processus dans l'ordre du journal.
  import { tick } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { provideActions } from '../../shell/workbench.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { processes } from '../../stores/live.svelte';
  import { makeContext, describeProposal } from '../../items';
  import {
    graph,
    errorMessage,
    formatDate,
    formatDuration,
    formatInt,
    formatTime,
    int,
    shortId,
    JAEGER_URL,
    type ChangeItem,
    type ExecutionRecord,
    type JsonValue,
  } from '../../api';

  let { tab }: { tab: Tab } = $props();

  const changeId = $derived(tab.params.id ?? '');
  /** restreint le journal à un processus (ouverture depuis une exécution) */
  const onlyProcess = $derived(tab.params.process ?? '');
  /** enregistrement à mettre en évidence (provenance d'un item) */
  const focus = $derived(tab.params.record ?? '');

  let records = $state<ExecutionRecord[]>([]);
  let items = $state<ChangeItem[]>([]);
  let loading = $state(false);
  let loaded = $state(false);
  let error = $state('');
  let showTicks = $state(true);
  let root = $state<HTMLElement>();

  async function load(id: string, pid: string, signal?: AbortSignal) {
    loading = true;
    error = '';
    try {
      const [j, c] = await Promise.all([
        graph.listExecutions(id, pid ? [pid] : [], signal),
        graph.getChange(id, signal).catch(() => ({ change: undefined })),
      ]);
      records = j.records ?? [];
      items = c.change?.items ?? [];
      loaded = true;
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  $effect(() => {
    const id = changeId;
    const pid = onlyProcess;
    records = [];
    loaded = false;
    if (!id) return;
    const ctrl = new AbortController();
    void load(id, pid, ctrl.signal);
    return () => ctrl.abort();
  });

  // Rechargement quand un processus du changement évolue (flux WatchEvents).
  const liveSignature = $derived(
    [...processes.values()]
      .filter((p) => p.changeId === changeId && (!onlyProcess || p.id === onlyProcess))
      .map((p) => `${p.id}:${p.status}:${p.steps?.length ?? 0}:${p.updatedAt ?? ''}`)
      .join('|'),
  );
  let lastSignature = '';
  $effect(() => {
    const sig = liveSignature;
    if (!loaded || sig === lastSignature) return;
    const first = lastSignature === '';
    lastSignature = sig;
    if (first) return;
    const timer = setTimeout(() => void load(changeId, onlyProcess), 400);
    return () => clearTimeout(timer);
  });

  // Mise en évidence de l'enregistrement demandé.
  let scrolledTo = '';
  $effect(() => {
    const id = focus;
    if (!id || !records.length || !root || scrolledTo === id) return;
    scrolledTo = id;
    void tick().then(() => root?.querySelector(`[data-record="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'center' }));
  });

  // --- regroupement par processus -------------------------------------------------------

  interface Group {
    processId: string;
    parentProcessId: string;
    agent: string;
    methodology: string;
    version: string;
    planner: string;
    goal: string;
    status: string;
    records: ExecutionRecord[];
    inputTokens: number;
    outputTokens: number;
    modelCalls: number;
    toolCalls: number;
    actions: number;
    durationMs: number;
    traceId: string;
  }

  function span(rs: ExecutionRecord[]): number {
    let start = Infinity;
    let end = -Infinity;
    for (const r of rs) {
      const s = r.startedAt ? Date.parse(r.startedAt) : NaN;
      const e = r.endedAt ? Date.parse(r.endedAt) : s;
      if (Number.isFinite(s)) start = Math.min(start, s);
      if (Number.isFinite(e)) end = Math.max(end, e);
    }
    return Number.isFinite(start) && Number.isFinite(end) && end >= start ? end - start : 0;
  }

  function summarize(pid: string, rs: ExecutionRecord[]): Group {
    const last = rs[rs.length - 1];
    const first = rs[0];
    const ended = [...rs].reverse().find((r) => r.kind === 'process.ended');
    const acts = rs.filter((r) => r.kind === 'action');
    let inTok = 0;
    let outTok = 0;
    let model = 0;
    let tools = 0;
    for (const r of acts) {
      inTok += int(r.inputTokens);
      outTok += int(r.outputTokens);
      model += r.modelCalls?.length ?? 0;
      tools += r.toolCalls?.length ?? 0;
    }
    // La fin de processus porte les totaux faisant foi (usage du processus).
    if (ended && (int(ended.inputTokens) || int(ended.outputTokens))) {
      inTok = int(ended.inputTokens);
      outTok = int(ended.outputTokens);
    }
    return {
      processId: pid,
      parentProcessId: first.parentProcessId ?? '',
      agent: last.agent || first.agent || '',
      methodology: last.methodology || '',
      version: last.methodologyVersion || '',
      planner: last.planner || '',
      goal: last.goal || '',
      status: ended?.status || processes.get(pid)?.status || last.status || '',
      records: rs,
      inputTokens: inTok,
      outputTokens: outTok,
      modelCalls: model,
      toolCalls: tools,
      actions: acts.length,
      durationMs: int(ended?.durationMs) || span(rs),
      traceId: rs.find((r) => r.traceId)?.traceId ?? '',
    };
  }

  const groups = $derived.by(() => {
    const byProcess = new Map<string, ExecutionRecord[]>();
    for (const r of records) {
      const pid = r.processId ?? '';
      let list = byProcess.get(pid);
      if (!list) byProcess.set(pid, (list = []));
      list.push(r);
    }
    return [...byProcess].map(([pid, rs]) => summarize(pid, rs));
  });

  const totals = $derived({
    inputTokens: groups.reduce((n, g) => n + g.inputTokens, 0),
    outputTokens: groups.reduce((n, g) => n + g.outputTokens, 0),
    modelCalls: groups.reduce((n, g) => n + g.modelCalls, 0),
    actions: groups.reduce((n, g) => n + g.actions, 0),
  });

  const ctx = $derived(makeContext([], items));

  // --- affichage d'un enregistrement ---------------------------------------------------

  const KIND_LABELS: Record<string, string> = {
    'process.started': 'début',
    tick: 'tick',
    action: 'action',
    approval: 'décision',
    'process.ended': 'fin',
  };

  function tone(r: ExecutionRecord): 'error' | 'ok' | 'partial' | 'pending' | 'neutral' {
    if (r.error) return 'error';
    if (r.kind === 'action') {
      if (waiting(r)) return 'pending';
      if (r.effectsMet === true) return 'ok';
      if (r.effectsMet === false) return 'partial';
      return 'neutral';
    }
    if (r.kind === 'process.ended') return r.status === 'completed' ? 'ok' : r.status === 'failed' ? 'error' : 'partial';
    if (r.kind === 'approval') return r.data?.['approved'] === false ? 'partial' : 'ok';
    return 'neutral';
  }

  function str(v: JsonValue | undefined): string {
    return typeof v === 'string' ? v : '';
  }

  function waiting(r: ExecutionRecord): string {
    return str(r.data?.['waiting']);
  }

  const WAITING_LABELS: Record<string, string> = {
    input: 'en attente de saisie',
    approval: "en attente d'approbation",
    agent: "en attente d'un sous-agent",
  };

  function unknownOf(r: ExecutionRecord): [string, string][] {
    const u = r.data?.['unknown'];
    if (!u || typeof u !== 'object' || Array.isArray(u)) return [];
    return Object.entries(u).map(([k, v]) => [k, typeof v === 'string' ? v : JSON.stringify(v)]);
  }

  function stringsOf(v: JsonValue | undefined): string[] {
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  }

  function numOf(v: JsonValue | undefined): number | undefined {
    return typeof v === 'number' ? v : undefined;
  }

  function itemLabel(id: string): string {
    const it = ctx.items.get(id);
    if (!it) return shortId(id);
    if (it.kind === 'proposal') return describeProposal(ctx, it);
    return `${it.kind ?? 'item'}${it.type ? ` ${it.type}` : ''}`;
  }

  function openRun(pid: string) {
    if (pid) openTab({ kind: 'run', params: { id: pid } });
  }

  function openChange() {
    openTab({ kind: 'change', params: { id: changeId } });
  }

  provideActions(
    () => tab.id,
    () => [
      { id: 'refresh', label: 'Actualiser', icon: 'refresh', disabled: loading, run: () => load(changeId, onlyProcess) },
      { id: 'change', label: 'Changement', icon: 'diff', disabled: !changeId, run: openChange },
      {
        id: 'all',
        label: 'Tous les processus',
        icon: 'filter',
        disabled: !onlyProcess,
        title: 'Afficher le journal de tous les processus du changement',
        run: () => {
          tab.params.process = '';
        },
      },
    ],
  );
</script>

<div class="editor-page wide" bind:this={root}>
  {#if error}<div class="alert">{error}</div>{/if}

  <div class="editor-head">
    <Icon name="list" size={18} />
    <h2>Journal d'exécution</h2>
    <button type="button" class="link mono" onclick={openChange}>changement {shortId(changeId)}</button>
    {#if onlyProcess}
      <span class="hint">· processus <code>{shortId(onlyProcess)}</code></span>
    {/if}
    <span class="grow"></span>
    <label class="check"><input type="checkbox" bind:checked={showTicks} /> Ticks</label>
  </div>

  {#if loading && !loaded}
    <p class="empty">Chargement…</p>
  {:else if loaded && !records.length}
    <p class="empty">Aucun enregistrement dans le journal de ce changement.</p>
  {/if}

  {#if groups.length > 1}
    <section class="stats" aria-label="Totaux du changement">
      <div class="stat"><span class="v">{groups.length}</span><span class="k">processus</span></div>
      <div class="stat"><span class="v">{totals.actions}</span><span class="k">actions</span></div>
      <div class="stat"><span class="v">{formatInt(totals.inputTokens)} → {formatInt(totals.outputTokens)}</span><span class="k">tokens entrée → sortie</span></div>
      <div class="stat"><span class="v">{totals.modelCalls}</span><span class="k">appels de modèle</span></div>
    </section>
  {/if}

  {#each groups as g (g.processId)}
    <section class="card process">
      <div class="row phead">
        <Icon name={g.parentProcessId ? 'bot' : 'runs'} size={16} />
        <strong>{g.agent || 'processus'}</strong>
        <button type="button" class="link mono" title="Ouvrir l'exécution" onclick={() => openRun(g.processId)}>{shortId(g.processId)}</button>
        <StatusBadge status={g.status} />
        {#if g.parentProcessId}
          <span class="hint">sous-agent de
            <button type="button" class="link mono" onclick={() => openRun(g.parentProcessId)}>{shortId(g.parentProcessId)}</button></span
          >
        {/if}
        <span class="grow"></span>
        {#if g.traceId}
          <a class="hint" href="{JAEGER_URL}/trace/{g.traceId}" target="_blank" rel="noreferrer"><Icon name="external" size={12} /> Trace</a>
        {/if}
      </div>
      <div class="hint pmeta">
        {#if g.methodology}{g.methodology}{g.version ? ` v${g.version}` : ''}{/if}
        {#if g.planner} · planificateur {g.planner}{/if}
        {#if g.goal} · objectif <code>{g.goal}</code>{/if}
      </div>
      <div class="totals">
        <span title="Tokens entrée → sortie"><strong>{formatInt(g.inputTokens)} → {formatInt(g.outputTokens)}</strong> tok</span>
        <span><strong>{g.modelCalls}</strong> appel{g.modelCalls > 1 ? 's' : ''} de modèle</span>
        {#if g.toolCalls}<span><strong>{g.toolCalls}</strong> appel{g.toolCalls > 1 ? 's' : ''} d'outil</span>{/if}
        <span><strong>{g.actions}</strong> action{g.actions > 1 ? 's' : ''}</span>
        <span>durée <strong>{formatDuration(g.durationMs)}</strong></span>
      </div>

      <ol class="timeline">
        {#each g.records as r (r.id ?? `${r.processId}:${r.seq}`)}
          {#if showTicks || r.kind !== 'tick'}
            {@const st = tone(r)}
            {@const w = waiting(r)}
            <li class="{st} k-{(r.kind ?? '').replace('.', '-')}" class:focus={!!r.id && r.id === focus} data-record={r.id}>
              <div class="dot" aria-hidden="true"></div>
              <div class="body">
                <div class="row head">
                  <span class="seq" title="Numéro d'ordre dans le journal du processus">{r.seq ?? ''}</span>
                  <span class="kind">{KIND_LABELS[r.kind ?? ''] ?? r.kind}</span>
                  {#if r.kind === 'tick'}
                    {#if r.plan?.length}
                      <ol class="plan" aria-label="Plan">
                        {#each r.plan as a, i (i)}<li class:chosen={i === 0}>{a}</li>{/each}
                      </ol>
                    {:else}
                      <span class="st">aucun plan</span>
                    {/if}
                    {#if r.data?.['replanned'] === true}<span class="tag warn" title="Le plan s'écarte de la suite du plan précédent">replanifié</span>{/if}
                    {#if numOf(r.data?.['candidates']) !== undefined}<span class="hint">{numOf(r.data?.['candidates'])} candidates</span>{/if}
                  {:else if r.kind === 'action'}
                    <code class="action">{r.action}</code>
                    {#if r.specialization}<span class="tag" title="Spécialisation exécutée à la place de l'action planifiée">→ {r.specialization}</span>{/if}
                    {#if r.actionKind}<span class="hint">{r.actionKind}</span>{/if}
                    {#if w}
                      <span class="st">{WAITING_LABELS[w] ?? `en attente (${w})`}</span>
                    {:else if r.effectsMet === true}
                      <span class="st" title="Effets atteints">✓ effets</span>
                    {:else if r.effectsMet === false}
                      <span class="st" title="Effets non atteints">✗ effets</span>
                    {:else if r.error}
                      <span class="st">erreur</span>
                    {/if}
                  {:else if r.kind === 'approval'}
                    <code class="action">{r.action}</code>
                    <span class="st">{r.data?.['approved'] === false ? 'refusée' : 'approuvée'}</span>
                    {#if str(r.data?.['comment'])}<span class="hint">« {str(r.data?.['comment'])} »</span>{/if}
                  {:else if r.kind === 'process.started'}
                    {#if str(r.data?.['title'])}<strong>{str(r.data?.['title'])}</strong>{/if}
                    {#if str(r.data?.['trigger'])}<span class="hint">déclenché par {str(r.data?.['trigger'])}</span>{/if}
                  {:else if r.kind === 'process.ended'}
                    <StatusBadge status={r.status} />
                    {#if numOf(r.data?.['steps']) !== undefined}<span class="hint">{numOf(r.data?.['steps'])} étape{(numOf(r.data?.['steps']) ?? 0) > 1 ? 's' : ''}</span>{/if}
                  {/if}
                  <span class="grow"></span>
                  {#if int(r.inputTokens) || int(r.outputTokens)}
                    <span class="usage" title="Tokens entrée / sortie">{formatInt(r.inputTokens)} → {formatInt(r.outputTokens)} tok</span>
                  {/if}
                  {#if r.modelCalls?.length}<span class="hint">{r.modelCalls.length} modèle{r.modelCalls.length > 1 ? 's' : ''}</span>{/if}
                  {#if r.toolCalls?.length}<span class="hint">{r.toolCalls.length} outil{r.toolCalls.length > 1 ? 's' : ''}</span>{/if}
                  {#if r.items?.length}<span class="hint">{r.items.length} item{r.items.length > 1 ? 's' : ''}</span>{/if}
                  {#if r.actor}<span class="hint">par {r.actor}</span>{/if}
                  {#if int(r.durationMs)}<span class="hint">{formatDuration(r.durationMs)}</span>{/if}
                  <span class="hint" title={formatDate(r.startedAt)}>{formatTime(r.startedAt)}</span>
                </div>

                {#if r.kind === 'process.started' && str(r.data?.['intent'])}
                  <p class="intent">« {str(r.data?.['intent'])} »</p>
                {/if}
                {#if r.kind === 'tick' && unknownOf(r).length}
                  <div class="unknown">
                    Conditions inconnues :
                    {#each unknownOf(r) as [c, why] (c)}<span class="chip" title={why}>{c}</span>{/each}
                  </div>
                {/if}
                {#if r.kind === 'process.ended'}
                  {#if stringsOf(r.data?.['disabled']).length}
                    <div class="unknown">
                      Actions désactivées :
                      {#each stringsOf(r.data?.['disabled']) as a (a)}<span class="chip">{a}</span>{/each}
                    </div>
                  {/if}
                {/if}
                {#if r.error}<pre class="error">{r.error}</pre>{/if}
                {#if str(r.data?.['child']) || stringsOf(r.data?.['children']).length}
                  <div class="children">
                    Sous-agents :
                    {#each [...new Set([str(r.data?.['child']), ...stringsOf(r.data?.['children'])].filter(Boolean))] as c (c)}
                      <button type="button" class="link mono" onclick={() => openRun(c)}>{processes.get(c)?.agent || shortId(c)}</button>
                    {/each}
                  </div>
                {/if}
                {#if r.items?.length}
                  <details>
                    <summary>Items produits ({r.items.length})</summary>
                    <ul class="items">
                      {#each r.items as it (it)}
                        {@const ci = ctx.items.get(it)}
                        <li class:superseded={ci?.status === 'superseded'}>
                          <code>{shortId(it)}</code> {itemLabel(it)}
                          {#if ci?.status}<StatusBadge status={ci.status} />{/if}
                        </li>
                      {/each}
                    </ul>
                  </details>
                {/if}
                {#if r.modelCalls?.length}
                  <details>
                    <summary>Appels de modèle ({r.modelCalls.length})</summary>
                    <table class="calls">
                      <thead>
                        <tr><th>Fournisseur</th><th>Modèle</th><th class="num">Entrée</th><th class="num">Sortie</th><th class="num">Durée</th><th>Erreur</th></tr>
                      </thead>
                      <tbody>
                        {#each r.modelCalls as c, k (k)}
                          <tr class:err={!!c.error}>
                            <td>{c.provider}</td>
                            <td><code>{c.model}</code></td>
                            <td class="num">{formatInt(c.inputTokens)}</td>
                            <td class="num">{formatInt(c.outputTokens)}</td>
                            <td class="num">{formatDuration(c.durationMs)}</td>
                            <td>{c.error ?? ''}</td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </details>
                {/if}
                {#if r.toolCalls?.length}
                  <details>
                    <summary>Appels d'outils ({r.toolCalls.length})</summary>
                    <table class="calls">
                      <thead><tr><th>Outil</th><th class="num">Durée</th><th>Erreur</th></tr></thead>
                      <tbody>
                        {#each r.toolCalls as c, k (k)}
                          <tr class:err={!!c.error}>
                            <td><code>{c.name}</code></td>
                            <td class="num">{formatDuration(c.durationMs)}</td>
                            <td>{c.error ?? ''}</td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </details>
                {/if}
                {#if r.output}
                  <details>
                    <summary>Sortie</summary>
                    <pre>{r.output}</pre>
                  </details>
                {/if}
              </div>
            </li>
          {/if}
        {/each}
      </ol>
    </section>
  {/each}
</div>

<style>
  .wide {
    max-width: 1400px;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.88rem;
    color: var(--muted);
  }
  .check input {
    margin: 0;
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
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
    font-size: 1.15rem;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }
  .stat .k {
    font-size: 0.82rem;
    color: var(--muted);
  }
  .phead {
    gap: 0.45rem;
  }
  .pmeta {
    margin: 0.2rem 0 0.4rem;
  }
  .totals {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 1.1rem;
    font-size: 0.88rem;
    color: var(--muted);
    padding: 0.4rem 0 0.6rem;
    border-bottom: 1px solid var(--border);
    margin-bottom: 0.6rem;
  }
  .totals strong {
    color: var(--text);
    font-variant-numeric: tabular-nums;
  }
  .timeline {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .timeline > li {
    display: grid;
    grid-template-columns: 1rem 1fr;
    gap: 0.6rem;
    position: relative;
    padding: 0.15rem 0.2rem 0.55rem 0;
    border-radius: var(--radius-sm);
  }
  .timeline > li:not(:last-child)::before {
    content: '';
    position: absolute;
    left: calc(0.5rem - 1px);
    top: 1.1rem;
    bottom: 0;
    width: 2px;
    background: var(--border);
  }
  .timeline > li.focus {
    background: var(--accent-soft);
  }
  .dot {
    width: 0.7rem;
    height: 0.7rem;
    margin: 0.3rem 0 0 0.15rem;
    border-radius: 50%;
    background: var(--muted);
  }
  .k-tick .dot {
    width: 0.5rem;
    height: 0.5rem;
    margin: 0.4rem 0 0 0.25rem;
    background: var(--border);
  }
  .ok .dot {
    background: var(--ok);
  }
  .partial .dot {
    background: var(--warn);
  }
  .error .dot {
    background: var(--danger);
  }
  .pending .dot {
    background: var(--accent);
  }
  .body {
    min-width: 0;
  }
  .head {
    gap: 0.45rem;
  }
  .seq {
    color: var(--muted);
    font-size: 0.8rem;
    font-variant-numeric: tabular-nums;
    min-width: 1.4rem;
  }
  .kind {
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: var(--muted);
    min-width: 3.4rem;
  }
  .action {
    font-weight: 600;
  }
  .st {
    font-size: 0.82rem;
    color: var(--muted);
  }
  .ok .st {
    color: var(--ok);
  }
  .partial .st {
    color: var(--warn);
  }
  .error .st {
    color: var(--danger);
  }
  .pending .st {
    color: var(--accent);
  }
  .tag {
    font-size: 0.78rem;
    font-family: var(--mono);
    background: var(--info-soft);
    color: var(--info);
    border-radius: 3px;
    padding: 0 0.3rem;
  }
  .tag.warn {
    font-family: inherit;
    background: var(--warn-soft);
    color: var(--warn);
  }
  .plan {
    list-style: none;
    display: inline-flex;
    flex-wrap: wrap;
    gap: 0.2rem;
    margin: 0;
    padding: 0;
    font-family: var(--mono);
    font-size: 0.82rem;
  }
  .plan li {
    color: var(--muted);
  }
  .plan li + li::before {
    content: '→ ';
  }
  .plan li.chosen {
    color: var(--text);
    font-weight: 600;
  }
  .usage {
    font-size: 0.82rem;
    font-variant-numeric: tabular-nums;
  }
  .intent {
    margin: 0.2rem 0;
    font-style: italic;
  }
  .unknown,
  .children {
    margin-top: 0.25rem;
    font-size: 0.88rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 0.5rem;
    align-items: baseline;
    color: var(--muted);
  }
  pre.error {
    margin-top: 0.3rem;
    color: var(--danger);
    background: var(--danger-soft);
    border-color: transparent;
  }
  details {
    margin-top: 0.25rem;
  }
  summary {
    cursor: pointer;
    font-size: 0.88rem;
    color: var(--muted);
  }
  details pre {
    margin-top: 0.3rem;
    max-height: 22rem;
  }
  .calls {
    margin-top: 0.25rem;
    font-size: 0.9em;
  }
  tr.err td {
    color: var(--danger);
  }
  .items {
    margin: 0.25rem 0 0;
    padding-left: 1.1rem;
    font-size: 0.9em;
  }
  .items li {
    margin: 0.1rem 0;
  }
  .items li.superseded {
    text-decoration: line-through;
    opacity: 0.6;
  }
</style>
