<script lang="ts">
  import {
    registry,
    graph,
    engine,
    errorMessage,
    compareVersions,
    type Baseline,
    type MethodologySummary,
    type Process,
  } from '../api';

  let { onstarted }: { onstarted: (p: Process) => void } = $props();

  let methodologies = $state<MethodologySummary[]>([]);
  let baselines = $state<Baseline[]>([]);
  let loadError = $state('');
  let loaded = $state(false);

  let methodology = $state('');
  let baselineId = $state('');
  let intent = $state('');
  let goal = $state('');
  let title = $state('');
  let submitting = $state(false);
  let error = $state('');

  $effect(() => {
    Promise.all([registry.listMethodologies(true), graph.listBaselines()])
      .then(([m, b]) => {
        methodologies = latestPublished(m.methodologies ?? []);
        loaded = true;
        baselines = b.baselines ?? [];
        if (!methodology && methodologies.length) methodology = methodologies[0].name ?? '';
        if (!baselineId && baselines.length) baselineId = baselines[baselines.length - 1].id ?? '';
      })
      .catch((e) => (loadError = errorMessage(e)));
  });

  /** Seules les versions publiées sont exécutables : on garde la plus récente par nom. */
  function latestPublished(list: MethodologySummary[]): MethodologySummary[] {
    const best = new Map<string, MethodologySummary>();
    for (const m of list) {
      if (m.status !== 'published' || !m.name) continue;
      const cur = best.get(m.name);
      if (!cur || compareVersions(m.version, cur.version) > 0) best.set(m.name, m);
    }
    return [...best.values()].sort((a, b) => (a.name ?? '').localeCompare(b.name ?? ''));
  }

  const goals = $derived(methodologies.find((m) => m.name === methodology)?.goals ?? []);

  // Réinitialise l'objectif si la méthodologie change et ne le propose plus.
  $effect(() => {
    if (goal && !goals.some((g) => g.name === goal)) goal = '';
  });

  const canSubmit = $derived(!!methodology && !!baselineId && !!intent.trim() && !submitting);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!canSubmit) return;
    submitting = true;
    error = '';
    try {
      const res = await engine.startProcess({
        methodology,
        baselineId,
        intent: intent.trim(),
        ...(title.trim() ? { title: title.trim() } : {}),
        ...(goal ? { goal } : {}),
      });
      if (res.process) {
        intent = '';
        title = '';
        onstarted(res.process);
      }
    } catch (err) {
      error = errorMessage(err);
    } finally {
      submitting = false;
    }
  }
</script>

<form class="card" onsubmit={submit}>
  <h3>Démarrer un processus</h3>
  {#if loadError}<div class="alert">{loadError}</div>{/if}

  <div class="grid">
    <div class="field">
      <label for="sp-meth">Méthodologie</label>
      <select id="sp-meth" bind:value={methodology} required>
        {#each methodologies as m (m.name)}
          <option value={m.name}>{m.name}{m.version ? ` (v${m.version})` : ''}</option>
        {/each}
      </select>
      {#if loaded && methodologies.length === 0}
        <div class="hint">Aucune méthodologie publiée : publiez-en une depuis l'écran « Méthodologies ».</div>
      {/if}
    </div>
    <div class="field">
      <label for="sp-base">Référentiel</label>
      <select id="sp-base" bind:value={baselineId} required>
        {#each baselines as b (b.id)}
          <option value={b.id}>{b.name || b.id}</option>
        {/each}
      </select>
    </div>
  </div>

  <div class="field">
    <label for="sp-intent">Intention</label>
    <textarea
      id="sp-intent"
      rows="3"
      bind:value={intent}
      placeholder="Ex. : la durée de session passe de 30 à 15 minutes, quel est l'impact ?"
      required
    ></textarea>
  </div>

  <div class="grid">
    <div class="field">
      <label for="sp-goal">Objectif</label>
      <select id="sp-goal" bind:value={goal}>
        <option value="">Déduit de l'intention</option>
        {#each goals as g (g.name)}
          <option value={g.name} title={g.description}>{g.name}</option>
        {/each}
      </select>
      {#if goal}
        <div class="hint">{goals.find((g) => g.name === goal)?.description}</div>
      {/if}
    </div>
    <div class="field">
      <label for="sp-title">Titre du changement <span class="opt">(facultatif)</span></label>
      <input id="sp-title" type="text" bind:value={title} />
    </div>
  </div>

  {#if error}<div class="alert">{error}</div>{/if}

  <button class="primary" type="submit" disabled={!canSubmit}>
    {submitting ? 'Démarrage…' : 'Démarrer'}
  </button>
</form>

<style>
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 0 1rem;
  }
  .opt {
    font-weight: 400;
  }
</style>
