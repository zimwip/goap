<script lang="ts" module>
  // Conservé quand le panneau est fermé puis rouvert.
  const session = $state({ recent: [] as string[], intent: '' });
</script>

<script lang="ts">
  // Outil « Tester » : envoie une intention (StartProcess) et affiche le
  // résultat de l'identification (candidats, question de clarification).
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import IntentDialogue from '../editors/IntentDialogue.svelte';
  import { engine, errorMessage, shortId, type Process } from '../../api';
  import {
    methodologies,
    baselines,
    refreshMethodologies,
    refreshBaselines,
    latestPublished,
  } from '../../stores/catalog.svelte';
  import { processes, ingestProcess } from '../../stores/live.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { focusRequests } from '../../shell/workbench.svelte';

  let methodology = $state('');
  /** « méthodologie::agent » */
  let agentKey = $state('');
  let baselineId = $state('');
  let goal = $state('');
  let title = $state('');
  let submitting = $state(false);
  let error = $state('');
  let textarea = $state<HTMLTextAreaElement>();

  $effect(() => {
    if (!methodologies.loaded) void refreshMethodologies();
    if (!baselines.loaded) void refreshBaselines();
  });

  $effect(() => {
    if (focusRequests.tester && textarea) {
      textarea.focus();
      textarea.scrollIntoView({ block: 'nearest' });
    }
  });

  const published = $derived(latestPublished());
  const agents = $derived(
    published
      .filter((m) => !methodology || m.name === methodology)
      .flatMap((m) => (m.agents ?? []).map((a) => ({ key: `${m.name}::${a.name}`, m: m.name ?? '', a }))),
  );
  const goals = $derived(published.find((m) => m.name === methodology)?.goals ?? []);

  // Valeurs par défaut et cohérence des sélections.
  $effect(() => {
    if (!baselineId && baselines.items.length) baselineId = baselines.items[baselines.items.length - 1].id ?? '';
  });
  $effect(() => {
    if (agentKey && !agents.some((a) => a.key === agentKey)) agentKey = '';
    if (goal && !goals.some((g) => g.name === goal)) goal = '';
  });

  const canSubmit = $derived(!!session.intent.trim() && !submitting && (!!baselineId || !baselines.items.length));
  const last = $derived<Process | undefined>(session.recent.length ? processes.get(session.recent[0]) : undefined);

  async function submit(e?: SubmitEvent) {
    e?.preventDefault();
    if (!canSubmit) return;
    submitting = true;
    error = '';
    const [am, a] = agentKey ? agentKey.split('::') : ['', ''];
    try {
      const res = await engine.startProcess({
        intent: session.intent.trim(),
        ...(methodology || am ? { methodology: methodology || am } : {}),
        ...(a ? { agent: a } : {}),
        ...(baselineId ? { baselineId } : {}),
        ...(title.trim() ? { title: title.trim() } : {}),
        ...(goal ? { goal } : {}),
      });
      const p = res.process;
      if (p?.id) {
        ingestProcess(p);
        session.recent = [p.id, ...session.recent.filter((r) => r !== p.id)].slice(0, 8);
        openTab({ kind: 'run', params: { id: p.id } }, { pin: true });
      }
    } catch (err) {
      error = errorMessage(err);
    } finally {
      submitting = false;
    }
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      void submit();
    }
  }
</script>

<div class="tester">
  <form onsubmit={submit}>
    <div class="field">
      <label for="t-meth">Méthodologie</label>
      <select id="t-meth" bind:value={methodology}>
        <option value="">Toutes (identification automatique)</option>
        {#each published as m (m.name)}
          <option value={m.name}>{m.name} (v{m.version})</option>
        {/each}
      </select>
    </div>
    <div class="field">
      <label for="t-agent">Agent</label>
      <select id="t-agent" bind:value={agentKey}>
        <option value="">Identifié à partir de l'intention</option>
        {#each agents as x (x.key)}
          <option value={x.key} title={x.a.description}>{methodology ? '' : `${x.m} / `}{x.a.name}{x.a.planner ? ` · ${x.a.planner}` : ''}</option>
        {/each}
      </select>
    </div>
    {#if methodology && goals.length}
      <div class="field">
        <label for="t-goal">Objectif</label>
        <select id="t-goal" bind:value={goal}>
          <option value="">Déduit de l'intention</option>
          {#each goals as g (g.name)}<option value={g.name} title={g.description}>{g.name}</option>{/each}
        </select>
      </div>
    {/if}
    <div class="field">
      <label for="t-base">Référentiel</label>
      <select id="t-base" bind:value={baselineId}>
        {#if !baselines.items.length}<option value="">— aucun —</option>{/if}
        {#each [...baselines.items].reverse() as b (b.id)}<option value={b.id}>{b.name || shortId(b.id)}</option>{/each}
      </select>
    </div>
    <div class="field">
      <label for="t-title">Titre du changement <span class="opt">(facultatif)</span></label>
      <input id="t-title" type="text" bind:value={title} />
    </div>
    <div class="field">
      <label for="t-intent">Intention</label>
      <textarea
        id="t-intent"
        bind:this={textarea}
        rows="5"
        bind:value={session.intent}
        onkeydown={keydown}
        placeholder="Ex. : la durée de session passe de 30 à 15 minutes, quel est l'impact ?"
      ></textarea>
      <div class="hint">Ctrl+Entrée pour envoyer.</div>
    </div>
    {#if methodologies.error}<div class="alert">{methodologies.error}</div>{/if}
    {#if methodologies.loaded && !published.length}
      <div class="alert info">Aucune méthodologie publiée : publiez-en une pour pouvoir l'exécuter.</div>
    {/if}
    {#if error}<div class="alert">{error}</div>{/if}
    <button class="primary send" type="submit" disabled={!canSubmit}>
      <Icon name="send" size={14} />{submitting ? 'Envoi…' : 'Envoyer'}
    </button>
  </form>

  {#if last}
    <section class="result">
      <div class="row">
        <strong class="grow">Résultat</strong>
        <StatusBadge status={last.status} />
      </div>
      <dl class="meta">
        <dt>Processus</dt>
        <dd><button type="button" class="link mono" onclick={() => openTab({ kind: 'run', params: { id: last.id ?? '' } }, { pin: true })}>{shortId(last.id)}</button></dd>
        {#if last.methodology}<dt>Méthodologie</dt><dd>{last.methodology}</dd>{/if}
        {#if last.agent}<dt>Agent</dt><dd><code>{last.agent}</code></dd>{/if}
        {#if last.goal}<dt>Objectif</dt><dd><code>{last.goal}</code></dd>{/if}
      </dl>
      {#if last.candidates?.length && last.status !== 'clarifying'}
        <table class="cands">
          <thead><tr><th>Agent</th><th>Objectif</th><th class="num">Confiance</th></tr></thead>
          <tbody>
            {#each last.candidates as c, i (i)}
              <tr title={c.reason}>
                <td>{c.methodology ? `${c.methodology} / ` : ''}{c.agent ?? ''}</td>
                <td><code>{c.goal ?? ''}</code></td>
                <td class="num">{Math.round((c.confidence ?? 0) * 100)} %</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
      {#if last.status === 'clarifying'}
        <IntentDialogue process={last} onupdate={ingestProcess} />
      {/if}
      {#if last.error}<pre class="perr">{last.error}</pre>{/if}
    </section>
  {/if}

  {#if session.recent.length > 1}
    <section class="recent">
      <h3>Tests récents</h3>
      <ul>
        {#each session.recent.slice(1) as id (id)}
          {@const p = processes.get(id)}
          <li>
            <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id } })}>{p?.agent || shortId(id)}</button>
            {#if p}<StatusBadge status={p.status} />{/if}
          </li>
        {/each}
      </ul>
    </section>
  {/if}
</div>

<style>
  .tester {
    padding: 0 0.8rem 1rem;
  }
  .send {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    width: 100%;
    justify-content: center;
  }
  .result {
    margin-top: 0.9rem;
    border-top: 1px solid var(--border);
    padding-top: 0.6rem;
  }
  .result .meta {
    margin: 0.4rem 0 0.6rem;
  }
  .result :global(.card) {
    padding: 0.5rem 0.6rem;
  }
  .result :global(table) {
    font-size: 0.9em;
  }
  .perr {
    color: var(--danger);
    background: var(--danger-soft);
    border-color: transparent;
  }
  .recent h3 {
    font-size: 0.9rem;
    margin: 0.8rem 0 0.3rem;
  }
  .recent ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.2rem;
  }
  .recent li {
    display: flex;
    gap: 0.4rem;
    align-items: center;
  }
</style>
