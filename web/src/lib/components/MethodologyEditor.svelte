<script lang="ts">
  // Éditeur d'une version de méthodologie. Un brouillon est modifiable ; une
  // version publiée ou archivée est affichée en lecture seule.
  import {
    registry,
    errorMessage,
    formatDate,
    bumpPatch,
    type Issue,
    type Methodology,
  } from '../api';
  import { go, href, setLeaveGuard } from '../nav.svelte';
  import {
    emptyForm,
    emptyNodeType,
    emptyLinkType,
    emptyCondition,
    emptyAction,
    emptyGoal,
    toForm,
    fromForm,
    conditionNames,
    normalizePath,
    parentPath,
    moveItem,
    type MethodologyForm,
  } from '../methodologyForm';
  import StatusBadge from './StatusBadge.svelte';
  import RowTools from './RowTools.svelte';
  import CondRows from './CondRows.svelte';
  import ActionEditor from './ActionEditor.svelte';

  /** `name` = « new » sans version : nouvelle méthodologie jamais enregistrée. */
  let { name, version }: { name: string; version: string } = $props();

  const LEAVE_MESSAGE = 'Des modifications ne sont pas enregistrées. Quitter quand même ?';

  let form = $state<MethodologyForm>(emptyForm());
  let snapshot = $state(JSON.stringify(emptyForm()));
  let isNew = $state(false);
  let status = $state('draft');
  let meta = $state<Pick<Methodology, 'createdAt' | 'updatedAt' | 'publishedAt' | 'updatedBy'>>({});

  let loading = $state(true);
  let busy = $state('');
  let error = $state('');
  let loadError = $state('');
  let notice = $state('');
  /** null : pas encore validé */
  let issues = $state<Issue[] | null>(null);
  let localIssues = $state<Issue[]>([]);

  const readonly = $derived(status !== 'draft');
  const dirty = $derived(JSON.stringify(form) !== snapshot);
  const allIssues = $derived([...localIssues, ...(issues ?? [])].map((i) => ({ ...i, norm: normalizePath(i.path) })));
  const condOptions = $derived(conditionNames(form));
  const nodeTypeNames = $derived([...new Set(form.nodeTypes.map((n) => n.name.trim()).filter(Boolean))]);
  const linkTypeNames = $derived([...new Set(form.linkTypes.map((l) => l.name.trim()).filter(Boolean))]);
  const canPublish = $derived(
    !readonly && !isNew && !dirty && !busy && issues !== null && allIssues.length === 0,
  );

  // --- chargement -------------------------------------------------------------

  function apply(m: Methodology) {
    form = toForm(m);
    snapshot = JSON.stringify(form);
    status = m.status || 'draft';
    meta = { createdAt: m.createdAt, updatedAt: m.updatedAt, publishedAt: m.publishedAt, updatedBy: m.updatedBy };
  }

  async function load() {
    loading = true;
    error = '';
    loadError = '';
    issues = null;
    localIssues = [];
    if (name === 'new' && !version) {
      isNew = true;
      form = emptyForm();
      snapshot = JSON.stringify(form);
      status = 'draft';
      meta = {};
      loading = false;
      return;
    }
    try {
      const res = await registry.getMethodology(name, version);
      if (!res.methodology) throw new Error('Méthodologie introuvable.');
      apply(res.methodology);
      loading = false;
      if (status === 'draft') await validate();
    } catch (e) {
      loadError = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  // --- garde de sortie ------------------------------------------------------------

  $effect(() => setLeaveGuard(() => !dirty || confirm(LEAVE_MESSAGE)));

  $effect(() => {
    const onUnload = (e: BeforeUnloadEvent) => {
      if (!dirty) return;
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', onUnload);
    return () => window.removeEventListener('beforeunload', onUnload);
  });

  // --- problèmes --------------------------------------------------------------

  function under(issuePath: string, path: string): boolean {
    return issuePath === path || issuePath.startsWith(`${path}.`) || issuePath.startsWith(`${path}[`);
  }

  /** Le champ `path` (ou, sauf `exact`, un de ses descendants) a-t-il un problème ? */
  function bad(path: string, exact = false): boolean {
    return allIssues.some((i) => (exact ? i.norm === path : under(i.norm, path)));
  }

  function count(path: string): number {
    return allIssues.filter((i) => under(i.norm, path)).length;
  }

  /** Amène à l'écran le champ correspondant au chemin (ou son plus proche parent). */
  function reveal(path: string) {
    const els = [...document.querySelectorAll<HTMLElement>('[data-path]')];
    for (let p = path; p; p = parentPath(p)) {
      const el = els.find((e) => e.dataset.path === p);
      if (!el) continue;
      el.scrollIntoView({ behavior: 'smooth', block: 'center' });
      if (el.matches('input, select, textarea')) el.focus({ preventScroll: true });
      return;
    }
  }

  function scrollTo(id: string) {
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  // --- actions de la barre d'outils -----------------------------------------------

  /** Construit le message ; renvoie null si le formulaire contient des erreurs locales. */
  function build(): Methodology | null {
    const { methodology, issues: li } = fromForm(form);
    localIssues = li;
    if (li.length) {
      error = 'Corrigez les erreurs signalées avant de continuer.';
      return null;
    }
    return methodology;
  }

  async function run(what: string, fn: () => Promise<void>) {
    busy = what;
    error = '';
    notice = '';
    try {
      await fn();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  async function validate() {
    const m = build();
    if (!m) return;
    await run('validate', async () => {
      issues = (await registry.validateMethodology(m)).issues ?? [];
    });
  }

  async function save() {
    if (!form.name.trim() || !form.version.trim()) {
      error = 'Le nom et la version sont obligatoires.';
      reveal(!form.name.trim() ? 'name' : 'version');
      return;
    }
    const m = build();
    if (!m) return;
    let done = false as boolean;
    await run('save', async () => {
      const res = await registry.saveMethodology(m);
      const saved = res.methodology ?? m;
      if (isNew) {
        // La route change : l'éditeur est rechargé depuis le serveur.
        snapshot = JSON.stringify(form);
        go('methodologies', saved.name || form.name.trim(), saved.version || form.version.trim());
        return;
      }
      apply(saved);
      issues = res.issues ?? [];
      done = true;
    });
    if (!done) return;
    // Validation automatique après l'enregistrement.
    await validate();
    if (!error) notice = 'Brouillon enregistré.';
  }

  async function publish() {
    if (!confirm(`Publier ${form.name} v${form.version} ? La version deviendra immuable et exécutable par le moteur.`))
      return;
    await run('publish', async () => {
      const res = await registry.publishMethodology(name, version);
      if (res.methodology) apply(res.methodology);
      else status = 'published';
      notice = 'Version publiée.';
    });
  }

  async function newVersion() {
    if (dirty && !confirm(`${LEAVE_MESSAGE}\nLa nouvelle version est copiée depuis la version enregistrée.`)) return;
    const v = prompt(`Numéro de la nouvelle version (copie de v${version}) :`, bumpPatch(version))?.trim();
    if (!v) return;
    await run('version', async () => {
      const res = await registry.createVersion(name, version, v);
      snapshot = JSON.stringify(form);
      go('methodologies', res.methodology?.name ?? name, res.methodology?.version ?? v);
    });
  }

  async function exportYaml() {
    await run('export', async () => {
      const res = await registry.exportMethodology(name, version);
      const blob = new Blob([res.yaml ?? ''], { type: 'application/yaml' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = res.filename || `${name}-${version}.yaml`;
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    });
  }

  async function remove() {
    const draft = status === 'draft';
    const msg = draft
      ? `Supprimer définitivement le brouillon ${name} v${version} ?`
      : `Archiver ${name} v${version} ? Elle ne pourra plus être utilisée pour démarrer un processus.`;
    if (!confirm(msg)) return;
    await run('delete', async () => {
      await registry.deleteMethodology(name, version);
      if (draft) {
        snapshot = JSON.stringify(form);
        go('methodologies');
      } else {
        await load();
        notice = 'Version archivée.';
      }
    });
  }

  function cancelNew() {
    go('methodologies');
  }

  const SECTIONS = [
    { id: 'm-general', label: 'Général' },
    { id: 'm-domain', label: 'Domaine' },
    { id: 'm-conditions', label: 'Conditions' },
    { id: 'm-actions', label: 'Actions' },
    { id: 'm-goals', label: 'Objectifs' },
  ];
</script>

<a class="back" href={href('methodologies')}>← Méthodologies</a>

{#if loading}
  <p class="empty">Chargement…</p>
{:else if loadError}
  <div class="alert">{loadError}</div>
{:else}
  <div class="toolbar">
    <div class="row title">
      <h2>{form.name || 'Nouvelle méthodologie'}</h2>
      {#if form.version}<span class="chip">v{form.version}</span>{/if}
      <StatusBadge {status} />
      {#if dirty}<span class="dirty">● modifications non enregistrées</span>{/if}
    </div>
    <div class="row buttons">
      {#if !readonly}
        <button type="button" onclick={validate} disabled={!!busy}>
          {busy === 'validate' ? 'Validation…' : 'Valider'}
        </button>
        <button type="button" class="primary" onclick={save} disabled={!!busy || (!dirty && !isNew)}>
          {busy === 'save' ? 'Enregistrement…' : 'Enregistrer le brouillon'}
        </button>
        <button
          type="button"
          onclick={publish}
          disabled={!canPublish}
          title={canPublish
            ? 'Figer cette version'
            : 'Enregistrez et validez le brouillon (sans problème) pour pouvoir le publier'}
        >
          {busy === 'publish' ? 'Publication…' : 'Publier'}
        </button>
      {/if}
      {#if !isNew}
        <button type="button" onclick={newVersion} disabled={!!busy}>Nouvelle version</button>
        <button type="button" onclick={exportYaml} disabled={!!busy}>Exporter YAML</button>
        {#if status !== 'archived'}
          <button type="button" class="danger" onclick={remove} disabled={!!busy}>
            {status === 'draft' ? 'Supprimer' : 'Archiver'}
          </button>
        {/if}
      {:else}
        <button type="button" onclick={cancelNew}>Annuler</button>
      {/if}
    </div>
    <div class="row jump">
      {#each SECTIONS as s (s.id)}
        <button type="button" class="small linkish" onclick={() => scrollTo(s.id)}>{s.label}</button>
      {/each}
    </div>
  </div>

  {#if error}<div class="alert">{error}</div>{/if}
  {#if notice}<div class="alert ok">{notice}</div>{/if}
  {#if readonly}
    <div class="alert info">
      {status === 'published'
        ? 'Version publiée : elle est immuable. Créez une nouvelle version pour la modifier.'
        : 'Version archivée : lecture seule.'}
    </div>
  {/if}

  {#if !readonly && (issues !== null || localIssues.length)}
    <section class="card issues" class:clean={allIssues.length === 0}>
      {#if allIssues.length === 0}
        <strong>Aucun problème détecté.</strong>
        {#if dirty}<span class="hint">(dernière validation — le formulaire a été modifié depuis)</span>{/if}
      {:else}
        <h3>{allIssues.length} problème{allIssues.length > 1 ? 's' : ''}</h3>
        <ul>
          {#each allIssues as i, k (k)}
            <li>
              <button type="button" class="issue" onclick={() => reveal(i.norm)}>
                {#if i.path}<code>{i.path}</code>{/if}
                <span>{i.message}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {/if}

  <fieldset disabled={readonly}>
    <!-- Général -------------------------------------------------------------- -->
    <section class="card" id="m-general">
      <h3>Général</h3>
      <div class="grid">
        <div class="field">
          <label for="m-name">Nom</label>
          <input
            id="m-name"
            type="text"
            class="mono"
            bind:value={form.name}
            disabled={!isNew}
            class:bad={bad('name')}
            data-path="name"
            placeholder="impact-analysis"
          />
        </div>
        <div class="field">
          <label for="m-version">Version</label>
          <input
            id="m-version"
            type="text"
            class="mono"
            bind:value={form.version}
            disabled={!isNew}
            class:bad={bad('version')}
            data-path="version"
            placeholder="0.1.0"
          />
        </div>
      </div>
      <div class="field">
        <label for="m-desc">Description</label>
        <textarea
          id="m-desc"
          rows="3"
          bind:value={form.description}
          class:bad={bad('description')}
          data-path="description"
        ></textarea>
      </div>
      {#if meta.updatedAt || meta.publishedAt}
        <p class="hint meta">
          {#if meta.createdAt}Créée le {formatDate(meta.createdAt)}.{/if}
          {#if meta.updatedAt}Modifiée le {formatDate(meta.updatedAt)}{meta.updatedBy
              ? ` par ${meta.updatedBy}`
              : ''}.{/if}
          {#if meta.publishedAt}Publiée le {formatDate(meta.publishedAt)}.{/if}
        </p>
      {/if}
    </section>

    <!-- Domaine -------------------------------------------------------------- -->
    <section class="card" id="m-domain">
      <h3>Domaine</h3>
      <h4 data-path="nodeTypes">Types de nœuds</h4>
      {#each form.nodeTypes as n, i}
        <div class="item" class:has-issues={count(`nodeTypes[${i}]`) > 0} data-path="nodeTypes[{i}]">
          <div class="grid3">
            <div class="field">
              <label for="nt-{i}-name">Nom</label>
              <input
                id="nt-{i}-name"
                type="text"
                class="mono"
                bind:value={n.name}
                class:bad={bad(`nodeTypes[${i}].name`)}
                data-path="nodeTypes[{i}].name"
                placeholder="Requirement"
              />
            </div>
            <div class="field">
              <label for="nt-{i}-desc">Description</label>
              <input
                id="nt-{i}-desc"
                type="text"
                bind:value={n.description}
                class:bad={bad(`nodeTypes[${i}].description`)}
                data-path="nodeTypes[{i}].description"
              />
            </div>
            <div class="field">
              <label for="nt-{i}-props">Propriétés <span class="opt">(séparées par des virgules)</span></label>
              <input
                id="nt-{i}-props"
                type="text"
                class="mono"
                bind:value={n.properties}
                class:bad={bad(`nodeTypes[${i}].properties`)}
                data-path="nodeTypes[{i}].properties"
                placeholder="title, description"
              />
            </div>
          </div>
          {#if !readonly}
            <div class="item-tools">
              <RowTools
                index={i}
                count={form.nodeTypes.length}
                label="le type de nœud"
                onmove={(d) => moveItem(form.nodeTypes, i, d)}
                onremove={() => form.nodeTypes.splice(i, 1)}
              />
            </div>
          {/if}
        </div>
      {:else}
        <p class="empty">Aucun type de nœud.</p>
      {/each}
      {#if !readonly}
        <button type="button" class="small add" onclick={() => form.nodeTypes.push(emptyNodeType())}>
          + Type de nœud
        </button>
      {/if}

      <h4 class="sub" data-path="linkTypes">Types de liens</h4>
      {#each form.linkTypes as l, i}
        <div class="item" class:has-issues={count(`linkTypes[${i}]`) > 0} data-path="linkTypes[{i}]">
          <div class="grid3">
            <div class="field">
              <label for="lt-{i}-name">Nom</label>
              <input
                id="lt-{i}-name"
                type="text"
                class="mono"
                bind:value={l.name}
                class:bad={bad(`linkTypes[${i}].name`)}
                data-path="linkTypes[{i}].name"
                placeholder="verifies"
              />
            </div>
            <div class="field">
              <label for="lt-{i}-from">De</label>
              <select
                id="lt-{i}-from"
                bind:value={l.from}
                class:bad={bad(`linkTypes[${i}].from`)}
                data-path="linkTypes[{i}].from"
              >
                <option value="">—</option>
                {#if l.from && !nodeTypeNames.includes(l.from)}<option value={l.from}>{l.from} (inconnu)</option>{/if}
                {#each nodeTypeNames as t (t)}<option value={t}>{t}</option>{/each}
              </select>
            </div>
            <div class="field">
              <label for="lt-{i}-to">Vers</label>
              <select
                id="lt-{i}-to"
                bind:value={l.to}
                class:bad={bad(`linkTypes[${i}].to`)}
                data-path="linkTypes[{i}].to"
              >
                <option value="">—</option>
                {#if l.to && !nodeTypeNames.includes(l.to)}<option value={l.to}>{l.to} (inconnu)</option>{/if}
                {#each nodeTypeNames as t (t)}<option value={t}>{t}</option>{/each}
              </select>
            </div>
          </div>
          {#if !readonly}
            <div class="item-tools">
              <RowTools
                index={i}
                count={form.linkTypes.length}
                label="le type de lien"
                onmove={(d) => moveItem(form.linkTypes, i, d)}
                onremove={() => form.linkTypes.splice(i, 1)}
              />
            </div>
          {/if}
        </div>
      {:else}
        <p class="empty">Aucun type de lien.</p>
      {/each}
      {#if !readonly}
        <button type="button" class="small add" onclick={() => form.linkTypes.push(emptyLinkType())}>
          + Type de lien
        </button>
      {/if}
    </section>

    <!-- Conditions ------------------------------------------------------------ -->
    <section class="card" id="m-conditions" data-path="conditions">
      <h3>Conditions</h3>
      <p class="hint">
        Expressions CEL évaluées sur le tableau noir (<code>impacts</code>, <code>proposals</code>,
        <code>artifacts</code>, <code>change</code>…). Elles forment l'état du monde du planificateur.
      </p>
      {#each form.conditions as c, i}
        <div class="item" class:has-issues={count(`conditions[${i}]`) > 0} data-path="conditions[{i}]">
          <div class="grid">
            <div class="field">
              <label for="c-{i}-name">Nom</label>
              <input
                id="c-{i}-name"
                type="text"
                class="mono"
                bind:value={c.name}
                class:bad={bad(`conditions[${i}].name`)}
                data-path="conditions[{i}].name"
                placeholder="has_impacts"
              />
            </div>
            <div class="field">
              <label for="c-{i}-desc">Description</label>
              <input
                id="c-{i}-desc"
                type="text"
                bind:value={c.description}
                class:bad={bad(`conditions[${i}].description`)}
                data-path="conditions[{i}].description"
              />
            </div>
          </div>
          <div class="field">
            <label for="c-{i}-expr">Expression CEL</label>
            <textarea
              id="c-{i}-expr"
              class="mono"
              rows="2"
              spellcheck="false"
              bind:value={c.expr}
              class:bad={bad(`conditions[${i}].expr`)}
              data-path="conditions[{i}].expr"
              placeholder="size(impacts) > 0"
            ></textarea>
          </div>
          {#if !readonly}
            <div class="item-tools">
              <RowTools
                index={i}
                count={form.conditions.length}
                label="la condition"
                onmove={(d) => moveItem(form.conditions, i, d)}
                onremove={() => form.conditions.splice(i, 1)}
              />
            </div>
          {/if}
        </div>
      {:else}
        <p class="empty">Aucune condition.</p>
      {/each}
      {#if !readonly}
        <button type="button" class="small add" onclick={() => form.conditions.push(emptyCondition())}>
          + Condition
        </button>
      {/if}
    </section>

    <!-- Actions --------------------------------------------------------------- -->
    <section class="card" id="m-actions" data-path="actions">
      <h3>Actions</h3>
      {#each form.actions as a, i}
        {@const n = count(`actions[${i}]`)}
        <div class="item" class:has-issues={n > 0} data-path="actions[{i}]">
          <div class="item-head">
            <strong class="mono">{a.name || `action ${i + 1}`}</strong>
            <span class="chip">{a.kind}</span>
            {#if n}<span class="count">{n} problème{n > 1 ? 's' : ''}</span>{/if}
            <span class="grow"></span>
            {#if !readonly}
              <RowTools
                index={i}
                count={form.actions.length}
                label="l'action"
                onmove={(d) => moveItem(form.actions, i, d)}
                onremove={() => form.actions.splice(i, 1)}
              />
            {/if}
          </div>
          <ActionEditor
            bind:action={form.actions[i]}
            index={i}
            conditions={condOptions}
            nodeTypes={nodeTypeNames}
            linkTypes={linkTypeNames}
            {bad}
            {readonly}
          />
        </div>
      {:else}
        <p class="empty">Aucune action.</p>
      {/each}
      {#if !readonly}
        <button type="button" class="small add" onclick={() => form.actions.push(emptyAction())}>+ Action</button>
      {/if}
    </section>

    <!-- Objectifs ------------------------------------------------------------- -->
    <section class="card" id="m-goals" data-path="goals">
      <h3>Objectifs</h3>
      {#each form.goals as g, i}
        {@const n = count(`goals[${i}]`)}
        <div class="item" class:has-issues={n > 0} data-path="goals[{i}]">
          <div class="item-head">
            <strong class="mono">{g.name || `objectif ${i + 1}`}</strong>
            {#if n}<span class="count">{n} problème{n > 1 ? 's' : ''}</span>{/if}
            <span class="grow"></span>
            {#if !readonly}
              <RowTools
                index={i}
                count={form.goals.length}
                label="l'objectif"
                onmove={(d) => moveItem(form.goals, i, d)}
                onremove={() => form.goals.splice(i, 1)}
              />
            {/if}
          </div>
          <div class="grid">
            <div class="field">
              <label for="g-{i}-name">Nom</label>
              <input
                id="g-{i}-name"
                type="text"
                class="mono"
                bind:value={g.name}
                class:bad={bad(`goals[${i}].name`)}
                data-path="goals[{i}].name"
                placeholder="impact_report"
              />
            </div>
            <div class="field">
              <label for="g-{i}-value">Valeur</label>
              <input
                id="g-{i}-value"
                type="number"
                step="any"
                bind:value={g.value}
                class:bad={bad(`goals[${i}].value`)}
                data-path="goals[{i}].value"
              />
            </div>
          </div>
          <div class="field">
            <label for="g-{i}-desc">Description</label>
            <input
              id="g-{i}-desc"
              type="text"
              bind:value={g.description}
              class:bad={bad(`goals[${i}].description`)}
              data-path="goals[{i}].description"
            />
          </div>
          <div class="grid2">
            <div class="field">
              <label for="g-{i}-ex">Exemples d'intentions <span class="opt">(un par ligne)</span></label>
              <textarea
                id="g-{i}-ex"
                rows="3"
                bind:value={g.examples}
                class:bad={bad(`goals[${i}].examples`)}
                data-path="goals[{i}].examples"
              ></textarea>
            </div>
            <div class="field">
              <CondRows
                bind:rows={g.pre}
                options={condOptions}
                path="goals[{i}].pre"
                label="Conditions à atteindre"
                {bad}
                {readonly}
              />
            </div>
          </div>
        </div>
      {:else}
        <p class="empty">Aucun objectif.</p>
      {/each}
      {#if !readonly}
        <button type="button" class="small add" onclick={() => form.goals.push(emptyGoal())}>+ Objectif</button>
      {/if}
    </section>
  </fieldset>
{/if}

<style>
  .back {
    display: inline-block;
    font-size: 0.88rem;
    margin-bottom: 0.4rem;
  }
  .toolbar {
    position: sticky;
    top: 0;
    z-index: 5;
    background: var(--bg);
    padding: 0.4rem 0 0.5rem;
    margin-bottom: 0.8rem;
    border-bottom: 1px solid var(--border);
    display: grid;
    gap: 0.45rem;
  }
  .title {
    gap: 0.5rem;
  }
  .title h2 {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .buttons {
    gap: 0.4rem;
  }
  .jump {
    gap: 0.1rem;
  }
  .linkish {
    border: none;
    background: none;
    color: var(--accent);
    font-weight: 500;
  }
  .dirty {
    color: var(--warn);
    font-size: 0.82rem;
    font-weight: 600;
  }
  fieldset {
    border: none;
    margin: 0;
    padding: 0;
    min-width: 0;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 0 0.85rem;
  }
  .grid2 {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 0 0.85rem;
  }
  .grid3 {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
    gap: 0 0.85rem;
  }
  .opt {
    font-weight: 400;
  }
  .meta {
    margin: 0;
  }
  h4.sub {
    margin-top: 1.2rem;
  }
  .item {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.7rem 0.8rem 0.2rem;
    margin-bottom: 0.6rem;
    background: var(--surface);
    min-width: 0;
  }
  .item.has-issues {
    border-color: var(--danger);
    box-shadow: inset 3px 0 0 var(--danger);
  }
  .item-head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin-bottom: 0.6rem;
    min-width: 0;
  }
  .item-head strong {
    overflow-wrap: anywhere;
  }
  .item-head .grow {
    flex: 1;
  }
  .item-tools {
    display: flex;
    justify-content: flex-end;
    margin: -0.3rem 0 0.5rem;
  }
  .count {
    font-size: 0.78rem;
    font-weight: 600;
    color: var(--danger);
  }
  .add {
    margin-top: 0.2rem;
  }
  .issues {
    border-color: var(--danger);
  }
  .issues.clean {
    border-color: var(--ok);
    color: var(--ok);
    padding: 0.6rem 1rem;
  }
  .issues h3 {
    color: var(--danger);
  }
  .issues ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.2rem;
  }
  .issue {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.6rem;
    width: 100%;
    text-align: left;
    border: none;
    background: none;
    font-weight: 400;
    padding: 0.3rem 0.4rem;
  }
  .issue code {
    color: var(--danger);
  }
  .issue:hover:not(:disabled) {
    background: var(--danger-soft);
  }
</style>
