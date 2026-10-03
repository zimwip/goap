<script lang="ts">
  import { stamp, keyOf } from '../flux/signals.svelte';
  // Decision points of a change (ADR 0009 §4): the questions the change must settle, usually which option. The
  // decider rules them (decided, or undecidable with the questions that block them); open questions are answered
  // here or investigated by an agent; a ruling an agent made below the threshold is ratified here. A ruling made
  // here is a person's: it needs no ratification, and it is the only one an escalated point accepts.
  import { graph, errorMessage, formatDate, shortId, type DecisionPoint, type Flow } from '../api';
  import StatusBadge from './StatusBadge.svelte';

  let { changeId, closed = false, onchange }: { changeId: string; closed?: boolean; onchange?: () => void } = $props();

  let points = $state<DecisionPoint[]>([]);
  let options = $state<Flow[]>([]);
  let error = $state('');
  let busy = $state('');
  // new point
  let question = $state('');
  let criteria = $state('');
  let decider = $state<'agent' | 'human'>('agent');
  let onOptions = $state(true);
  let maxDuration = $state('');
  // per point / question drafts
  let answers = $state<Record<string, string>>({});
  let ruling = $state<Record<string, { outcome: string; option: string; justification: string; questions: string }>>({});
  let refusal = $state<Record<string, string>>({});

  const openOptions = $derived(options.filter((o) => o.status === 'open'));
  const nameOf = (id: string | undefined) => options.find((o) => o.id === id)?.option?.name ?? (id ? shortId(id) : '—');

  async function load(signal?: AbortSignal) {
    try {
      const ps = (await graph.listDecisionPoints(changeId, signal)).points ?? [];
      for (const d of ps) ruling[d.id ?? ''] ??= blank();
      points = ps;
      options = (await graph.listOptions(changeId, signal)).options ?? [];
      error = '';
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    }
  }

  $effect(() => {
    if (!changeId) return;
    void stamp(keyOf.change(changeId));
    const ctrl = new AbortController();
    load(ctrl.signal);
    return () => ctrl.abort();
  });

  async function act(label: string, fn: () => Promise<unknown>) {
    busy = label;
    error = '';
    try {
      await fn();
      await load();
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  const lines = (s: string) =>
    s
      .split('\n')
      .map((x) => x.trim())
      .filter(Boolean);

  const open = () =>
    act('open', async () => {
      await graph.openDecision({
        changeId,
        question: question.trim(),
        allOptions: onOptions,
        options: onOptions ? undefined : [],
        criteria: criteria.split(',').map((c) => c.trim()).filter(Boolean),
        decider,
        maxDuration: maxDuration.trim() || undefined,
      });
      question = '';
      criteria = '';
      maxDuration = '';
    });

  const blank = () => ({ outcome: 'decided', option: '', justification: '', questions: '' });

  const rule = (d: DecisionPoint) =>
    act(`rule:${d.id}`, async () => {
      const r = ruling[d.id ?? ''] ?? blank();
      await graph.ruleDecision({
        changeId,
        point: d.id ?? '',
        outcome: r.outcome,
        option: r.outcome === 'decided' ? r.option : undefined,
        confidence: 1,
        justification: r.justification.trim(),
        questions: r.outcome === 'undecidable' ? lines(r.questions) : undefined,
      });
      ruling[d.id ?? ''] = blank();
    });
</script>

<div class="decisions">
  {#if points.length}
    <ul class="list">
      {#each points as d (d.id)}
        {@const r = ruling[d.id ?? ''] ?? blank()}
        <li>
          <div class="head">
            <StatusBadge status={d.status} />
            <strong>{d.question}</strong>
            <code class="muted">{shortId(d.id)}</code>
          </div>
          <p class="meta muted">
            decider {d.decider} · threshold {d.threshold} · round {d.rounds}/{d.maxRounds}
            {#if d.deadline}· deadline {formatDate(d.deadline)}{/if}
            {#if d.options?.length}· among {d.options.map(nameOf).join(', ')}{/if}
            {#if d.criteria?.length}· criteria {d.criteria.join(', ')}{/if}
          </p>
          {#if d.escalation}<p class="alert warn">Escalated: {d.escalation}. Only a person rules it now.</p>{/if}
          {#if d.status === 'decided'}
            <p>
              Decided{#if d.option}: <strong>{nameOf(d.option)}</strong>{/if} by {d.decidedBy || d.ruling?.by || '—'}
              {#if d.ruling?.justification}<span class="muted">— {d.ruling.justification}</span>{/if}
            </p>
          {:else if d.ruling}
            <p class="muted">
              Last ruling: {d.ruling.outcome}{#if d.ruling.option} {nameOf(d.ruling.option)}{/if}
              {#if d.ruling.confidence}(confidence {d.ruling.confidence}){/if} by {d.ruling.by || 'an agent'}
              {#if d.ruling.justification}— {d.ruling.justification}{/if}
            </p>
          {/if}

          {#if d.questions?.length}
            <ul class="questions">
              {#each d.questions as q (q.id)}
                <li>
                  <StatusBadge status={q.status} />
                  {q.text}
                  {#if q.status === 'answered'}
                    <div class="answer">→ {q.answer} <span class="muted">({q.answeredBy || 'agent'}{q.process ? `, process ${shortId(q.process)}` : ''})</span></div>
                  {:else if !closed}
                    <div class="row">
                      <input type="text" class="grow" placeholder="Answer" bind:value={answers[q.id ?? '']} />
                      <button type="button" disabled={!!busy || !answers[q.id ?? '']?.trim()} onclick={() => act(`answer:${q.id}`, () => graph.answerQuestion(changeId, q.id ?? '', answers[q.id ?? ''].trim()))}>Answer</button>
                    </div>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}

          {#if !closed && d.status === 'ratifying'}
            <div class="row">
              <span>The agent's ruling waits for a person:</span>
              <button type="button" class="primary" disabled={!!busy} onclick={() => act(`ratify:${d.id}`, () => graph.ratifyDecision(changeId, d.id ?? '', true, 'ratified'))}>Ratify</button>
              <input type="text" placeholder="Why refuse" bind:value={refusal[d.id ?? '']} />
              <button type="button" class="danger" disabled={!!busy || !refusal[d.id ?? '']?.trim()} onclick={() => act(`refuse:${d.id}`, () => graph.ratifyDecision(changeId, d.id ?? '', false, refusal[d.id ?? ''].trim()))}>Refuse</button>
            </div>
          {/if}

          {#if !closed && (d.status === 'open' || d.status === 'escalated' || d.status === 'ratifying')}
            <form class="rule" onsubmit={(e) => (e.preventDefault(), rule(d))}>
              <select bind:value={r.outcome}>
                <option value="decided">decided</option>
                <option value="undecidable">undecidable</option>
              </select>
              {#if r.outcome === 'decided' && d.options?.length}
                <select bind:value={r.option}>
                  <option value="" disabled>option…</option>
                  {#each d.options as o (o)}<option value={o}>{nameOf(o)}</option>{/each}
                </select>
              {/if}
              <input type="text" class="grow" placeholder="Justification" bind:value={r.justification} />
              {#if r.outcome === 'undecidable'}
                <textarea rows="2" class="grow" placeholder="Questions to answer first, one per line" bind:value={r.questions}></textarea>
              {/if}
              <button
                type="submit"
                disabled={!!busy || !r.justification.trim() || (r.outcome === 'decided' && !!d.options?.length && !r.option) || (r.outcome === 'undecidable' && !lines(r.questions).length)}
                >Rule</button
              >
            </form>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p class="hint">No decision point: open one to settle a question of the change, among its options or not.</p>
  {/if}

  {#if !closed}
    <form class="new" onsubmit={(e) => (e.preventDefault(), open())}>
      <input type="text" class="grow" placeholder="Question to decide" bind:value={question} />
      <input type="text" class="crit" placeholder="Criteria (comma separated)" bind:value={criteria} />
      <select bind:value={decider} title="Who rules it">
        <option value="agent">agent decides</option>
        <option value="human">a person decides</option>
      </select>
      <input type="text" class="dur" placeholder="Deadline (48h)" bind:value={maxDuration} />
      <label class="check" title={openOptions.length ? '' : 'the change has no open option'}>
        <input type="checkbox" bind:checked={onOptions} disabled={!openOptions.length} /> among the {openOptions.length} open option(s)
      </label>
      <button type="submit" disabled={!!busy || !question.trim()}>Open decision</button>
    </form>
  {/if}

  {#if error}<div class="alert">{error}</div>{/if}
</div>

<style>
  .decisions {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .list,
  .questions {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .list > li {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 8px 10px;
  }
  .questions {
    margin: 6px 0;
    padding-left: 10px;
    border-left: 2px solid var(--border);
  }
  .head,
  .row,
  .new,
  .rule {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .rule {
    margin-top: 6px;
  }
  .meta {
    margin: 4px 0;
    font-size: 0.9em;
  }
  .answer {
    margin: 2px 0 0 4px;
  }
  .grow {
    flex: 1;
    min-width: 12em;
  }
  .new > input,
  .new > select,
  .rule > select {
    width: auto;
  }
  .new > input.crit {
    width: 14em;
  }
  .dur {
    width: 9em !important;
  }
  .check {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  .danger {
    color: var(--danger);
    border-color: var(--danger);
  }
</style>
