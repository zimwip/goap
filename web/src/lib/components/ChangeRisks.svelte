<script lang="ts">
  // Risks and actions of a change (ADR 0036 §1): the risk register (probability × impact, status, owner role, the
  // actions that mitigate it) with its heat map, and the actions that follow from risks and decisions. Every edit is a
  // new version of the record, kept in the change log with who made it.
  import { graph, errorMessage, type Change, type Struct } from '../api';
  import AssessmentBubble from './AssessmentBubble.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { riskRegister, actionList, liveRisk, unmitigated, covers, nextKey, pocData, RISK_STATUSES, ACTION_STATUSES, HIGH_RISK, KIND_POC, PURPOSE_DERISK, PURPOSE_EXPLORE } from '../risks';

  let { change, closed = false, onchange }: { change: Change; closed?: boolean; onchange?: () => void } = $props();

  const risks = $derived(riskRegister(change.items ?? []));
  const actions = $derived(actionList(change.items ?? []));
  const live = $derived(risks.filter(liveRisk));
  let error = $state('');
  let busy = $state(false);
  // new risk / action drafts
  let rTitle = $state('');
  let rDesc = $state('');
  let rP = $state(3);
  let rI = $state(3);
  let rOwner = $state('');
  let aTitle = $state('');
  let aOwner = $state('');
  let aDue = $state('');
  let aFor = $state('');
  // the kind of the new action: a mitigation, a proof of concept of risks, a prototype of the need (ADR 0131)
  let aKind = $state<'mitigation' | 'poc' | 'prototype'>('mitigation');
  let aRisks = $state<string[]>([]);
  let aStatements = $state('');
  const statements = (s: string) => s.split(/[\n;]/).map((x) => x.trim()).filter((x) => x);
  const canAdd = $derived(
    !!aTitle.trim() && (aKind === 'mitigation' || (aKind === 'poc' ? aRisks.length > 0 : statements(aStatements).length > 0)),
  );

  async function add(kind: 'risk' | 'action', data: Struct) {
    if (!change.id) return;
    busy = true;
    error = '';
    try {
      await graph.addItems(change.id, [{ type: kind, target: kind, data }]);
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  async function raise() {
    if (!rTitle.trim()) return;
    await add('risk', { key: nextKey('RSK-', risks.map((r) => r.key)), title: rTitle.trim(), description: rDesc.trim(), probability: rP, impact: rI, owner: rOwner.trim(), status: 'open' });
    rTitle = rDesc = rOwner = '';
  }

  async function newAction() {
    if (!canAdd) return;
    const key = nextKey('ACT-', actions.map((a) => a.key));
    if (aKind === 'mitigation') {
      await add('action', { key, title: aTitle.trim(), owner: aOwner.trim(), due: aDue, for: aFor, status: 'open' });
    } else {
      // only proposed: a person opens it as a sub-change from the proofs step of the change
      const purpose = aKind === 'poc' ? PURPOSE_DERISK : PURPOSE_EXPLORE;
      await add('action', { ...pocData(key, aTitle.trim(), purpose, aKind === 'poc' ? aRisks : [], statements(aStatements), aOwner.trim()), due: aDue });
    }
    aTitle = aOwner = aDue = aFor = aStatements = '';
    aRisks = [];
  }

  /** what a proof of concept checks, in words */
  const pocWhat = (a: { purpose: string; risks: string[]; hypotheses: string[] }) =>
    a.purpose === PURPOSE_EXPLORE ? `prototype: ${a.hypotheses.join('; ')}` : `POC of ${a.risks.join(', ')}`;

  // heat map: live risks by probability (rows, 5 on top) and impact (columns)
  const cell = (p: number, i: number) => live.filter((r) => r.probability === p && r.impact === i);
  const heat = (score: number) => (score >= 15 ? 'h3' : score >= HIGH_RISK ? 'h2' : score >= 5 ? 'h1' : 'h0');
</script>

<section class="card">
  <h3>Risks <span class="hint">{live.length} live · {risks.length} in all</span></h3>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="top">
    <table class="heat" aria-label="Heat map of the live risks">
      <tbody>
        {#each [5, 4, 3, 2, 1] as p (p)}
          <tr>
            <th scope="row">P{p}</th>
            {#each [1, 2, 3, 4, 5] as i (i)}
              {@const rs = cell(p, i)}
              <td class={heat(p * i)} title={rs.map((r) => `${r.key} ${r.title}`).join('\n')}>{rs.length || ''}</td>
            {/each}
          </tr>
        {/each}
        <tr><th></th>{#each [1, 2, 3, 4, 5] as i (i)}<th scope="col">I{i}</th>{/each}</tr>
      </tbody>
    </table>
    <div class="grow">
      {#if risks.length}
        <table class="reg">
          <thead><tr><th>Key</th><th>Risk</th><th>P×I</th><th>Status</th><th>Owner</th><th>Actions</th></tr></thead>
          <tbody>
            {#each risks as r (r.key)}
              <tr class:dim={!liveRisk(r)} class:warn={unmitigated(r, actions)}>
                <td class="mono">{r.key} <AssessmentBubble {change} subject={`item:${r.key}`} label="the risk {r.key}" compact {closed} {onchange} /></td>
                <td>{r.title}{#if r.description}<div class="hint">{r.description}</div>{/if}</td>
                <td class="mono"><span class="score {heat(r.score)}">{r.score}</span> {r.probability}×{r.impact}</td>
                <td>
                  <select value={r.status} disabled={closed || busy} aria-label="Status of {r.key}" onchange={(e) => add('risk', { key: r.key, title: r.title, status: (e.currentTarget as HTMLSelectElement).value })}>
                    {#each RISK_STATUSES as s (s)}<option value={s}>{s}</option>{/each}
                  </select>
                </td>
                <td>{r.owner || '—'}</td>
                <td>
                  {actions.filter((a) => a.for === r.key || a.risks.includes(r.key)).map((a) => `${a.key}${a.kind === KIND_POC ? ' POC' : ''} (${a.status}${covers(a, r.key) ? '' : ', covers no more'})`).join(', ') || (unmitigated(r, actions) ? 'none — needs one' : '—')}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}
        <p class="empty">No risk raised on this change.</p>
      {/if}
    </div>
  </div>
  {#if !closed}
    <form class="row add" onsubmit={(e) => (e.preventDefault(), raise())}>
      <input type="text" placeholder="New risk" bind:value={rTitle} aria-label="Risk title" />
      <input type="text" placeholder="Cause and consequence" bind:value={rDesc} aria-label="Risk description" />
      <label>P <input type="number" min="1" max="5" bind:value={rP} /></label>
      <label>I <input type="number" min="1" max="5" bind:value={rI} /></label>
      <input type="text" placeholder="Owner role" bind:value={rOwner} aria-label="Risk owner" />
      <button type="submit" class="small primary" disabled={busy || !rTitle.trim()}>Raise</button>
    </form>
  {/if}
</section>

<section class="card">
  <h3>Actions <span class="hint">{actions.filter((a) => a.status === 'open').length} open · {actions.length} in all</span></h3>
  {#if actions.length}
    <table class="reg">
      <thead><tr><th>Key</th><th>Action</th><th>For</th><th>Owner</th><th>Due</th><th>Status</th></tr></thead>
      <tbody>
        {#each actions as a (a.key)}
          <tr class:dim={a.status !== 'open'}>
            <td class="mono">{a.key} <AssessmentBubble {change} subject={`item:${a.key}`} label="the action {a.key}" compact {closed} {onchange} /></td>
            <td>
              {#if a.kind === KIND_POC}<span class="kind" title={pocWhat(a)}>{a.purpose === PURPOSE_EXPLORE ? 'prototype' : 'POC'}</span>{/if}
              {a.title}
              {#if a.kind === KIND_POC}
                <div class="hint">
                  {pocWhat(a)}{#if a.change} · <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: a.change } })}>its sub-change</button>{:else if a.status === 'open'} · to open from the Proofs step{/if}
                </div>
              {/if}
              {#if a.result}<div class="hint">{a.result}</div>{/if}
            </td>
            <td class="mono">{a.kind === KIND_POC && a.risks.length ? a.risks.join(', ') : a.for || '—'}</td>
            <td>{a.owner || '—'}</td>
            <td>{a.due || '—'}</td>
            <td>
              <select value={a.status} disabled={closed || busy} aria-label="Status of {a.key}" onchange={(e) => add('action', { key: a.key, title: a.title, status: (e.currentTarget as HTMLSelectElement).value })}>
                {#each ACTION_STATUSES as s (s)}<option value={s}>{s}</option>{/each}
              </select>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="empty">No action.</p>
  {/if}
  {#if !closed}
    <form class="row add" onsubmit={(e) => (e.preventDefault(), newAction())}>
      <select bind:value={aKind} aria-label="Kind of action" title="A proof of concept checks risks by building the least that shows them; a prototype checks the need (ADR 0131)">
        <option value="mitigation">Mitigation</option>
        <option value="poc">Proof of concept</option>
        <option value="prototype">Prototype</option>
      </select>
      <input type="text" placeholder={aKind === 'mitigation' ? 'New action' : aKind === 'poc' ? 'What the POC shows' : 'What the prototype explores'} bind:value={aTitle} aria-label="Action title" />
      {#if aKind === 'mitigation'}
        <select bind:value={aFor} aria-label="For">
          <option value="">— for —</option>
          {#each risks as r (r.key)}<option value={r.key}>{r.key} {r.title}</option>{/each}
        </select>
      {:else if aKind === 'poc'}
        <select multiple bind:value={aRisks} aria-label="Risks the POC checks" size={Math.min(4, Math.max(2, live.length))}>
          {#each live as r (r.key)}<option value={r.key}>{r.key} {r.title}</option>{/each}
        </select>
      {:else}
        <input type="text" placeholder="Statements to check, separated by ;" bind:value={aStatements} aria-label="Statements the prototype checks" />
      {/if}
      <input type="text" placeholder="Owner role" bind:value={aOwner} aria-label="Action owner" />
      <input type="date" bind:value={aDue} aria-label="Due" />
      <button type="submit" class="small primary" disabled={busy || !canAdd}>{aKind === 'mitigation' ? 'Add' : 'Propose'}</button>
    </form>
  {/if}
</section>

<style>
  .top {
    display: flex;
    gap: 16px;
    align-items: flex-start;
    flex-wrap: wrap;
  }
  .grow {
    flex: 1;
    min-width: 280px;
    overflow-x: auto;
  }
  .heat {
    width: auto;
    flex: none;
    border-collapse: collapse;
  }
  .heat td {
    width: 28px;
    min-width: 28px;
    height: 24px;
    text-align: center;
    border: 1px solid var(--border);
    font-weight: 600;
  }
  .heat th {
    font-size: 0.8em;
    color: var(--muted);
    font-weight: normal;
  }
  .h0 {
    background: var(--surface);
  }
  .h1 {
    background: var(--ok);
    color: #fff;
    opacity: 0.55;
  }
  .h2 {
    background: var(--warn);
    color: #fff;
  }
  .h3 {
    background: var(--danger);
    color: #fff;
  }
  .score {
    display: inline-block;
    min-width: 2em;
    text-align: center;
    border-radius: 6px;
  }
  .reg {
    width: 100%;
    border-collapse: collapse;
  }
  .reg th,
  .reg td {
    text-align: left;
    padding: 3px 6px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  .kind {
    font-size: 0.75em;
    padding: 0 5px;
    margin-right: 4px;
    border-radius: 6px;
    border: 1px solid var(--border);
    color: var(--muted);
  }
  tr.dim {
    opacity: 0.6;
  }
  tr.warn td:first-child {
    border-left: 3px solid var(--danger);
  }
  .add {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 8px;
    align-items: center;
  }
  .add input,
  .add select {
    width: auto;
    flex: 1 1 140px;
  }
  .add label {
    display: flex;
    align-items: center;
    gap: 4px;
    flex: none;
  }
  .add input[type='number'] {
    width: 3.5em;
    flex: none;
  }
  .add button {
    flex: none;
  }
</style>
