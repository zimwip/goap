<script lang="ts">
  // Liste de toutes les versions des méthodologies, regroupées par nom, et
  // import d'une définition YAML.
  import {
    registry,
    errorMessage,
    formatDate,
    compareVersions,
    type Issue,
    type Methodology,
    type MethodologySummary,
  } from '../api';
  import { go, href } from '../nav.svelte';
  import StatusBadge from './StatusBadge.svelte';

  let methodologies = $state<MethodologySummary[]>([]);
  let loading = $state(true);
  let error = $state('');

  interface Group {
    name: string;
    description: string;
    versions: MethodologySummary[];
  }

  const groups = $derived.by(() => {
    const byName = new Map<string, MethodologySummary[]>();
    for (const m of methodologies) {
      const k = m.name ?? '';
      byName.set(k, [...(byName.get(k) ?? []), m]);
    }
    const out: Group[] = [];
    for (const [name, versions] of byName) {
      versions.sort((a, b) => compareVersions(b.version, a.version));
      const ref = versions.find((v) => v.status === 'published') ?? versions[0];
      out.push({ name, description: ref?.description ?? '', versions });
    }
    return out.sort((a, b) => a.name.localeCompare(b.name));
  });

  async function load() {
    loading = true;
    error = '';
    try {
      methodologies = (await registry.listMethodologies(true)).methodologies ?? [];
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  // --- import ---------------------------------------------------------------------

  let importOpen = $state(false);
  let yaml = $state('');
  let publish = $state(false);
  let importing = $state(false);
  let importError = $state('');
  let imported = $state<{ methodology?: Methodology; issues: Issue[] } | null>(null);

  async function readFile(e: Event & { currentTarget: HTMLInputElement }) {
    const file = e.currentTarget.files?.[0];
    if (!file) return;
    try {
      yaml = await file.text();
    } catch (err) {
      importError = errorMessage(err);
    }
  }

  async function doImport(e: SubmitEvent) {
    e.preventDefault();
    if (!yaml.trim()) return;
    importing = true;
    importError = '';
    imported = null;
    try {
      const res = await registry.importMethodology(yaml, publish);
      imported = { methodology: res.methodology, issues: res.issues ?? [] };
      await load();
    } catch (err) {
      importError = errorMessage(err);
    } finally {
      importing = false;
    }
  }

  function closeImport() {
    importOpen = false;
    imported = null;
    importError = '';
  }
</script>

<div class="row head">
  <h2 class="grow">Méthodologies</h2>
  <button onclick={load} disabled={loading}>Actualiser</button>
  <button onclick={() => (importOpen ? closeImport() : (importOpen = true))}>Importer YAML</button>
  <button class="primary" onclick={() => go('methodologies', 'new')}>Nouvelle méthodologie</button>
</div>

{#if importOpen}
  <form class="card" onsubmit={doImport}>
    <h3>Importer une définition YAML</h3>
    <div class="field">
      <label for="imp-file">Fichier</label>
      <input id="imp-file" type="file" accept=".yaml,.yml,text/yaml,application/yaml" onchange={readFile} />
    </div>
    <div class="field">
      <label for="imp-yaml">… ou contenu YAML</label>
      <textarea id="imp-yaml" class="mono" rows="10" spellcheck="false" bind:value={yaml} placeholder="name: …"
      ></textarea>
    </div>
    <label class="check">
      <input type="checkbox" bind:checked={publish} />
      Publier directement <span class="opt">(la définition doit être valide)</span>
    </label>
    {#if importError}<div class="alert">{importError}</div>{/if}
    {#if imported}
      {@const m = imported.methodology}
      <div class="alert" class:ok={imported.issues.length === 0} class:warn={imported.issues.length > 0}>
        {#if m}
          Importée : <strong>{m.name}</strong> v{m.version} ({m.status === 'published' ? 'publiée' : 'brouillon'}).
          <a href={href('methodologies', m.name, m.version)}>Ouvrir →</a>
        {:else}
          Import terminé.
        {/if}
      </div>
      {#if imported.issues.length}
        <ul class="issues">
          {#each imported.issues as i, k (k)}
            <li>{#if i.path}<code>{i.path}</code> {/if}{i.message}</li>
          {/each}
        </ul>
      {/if}
    {/if}
    <div class="row">
      <button class="primary" type="submit" disabled={importing || !yaml.trim()}>
        {importing ? 'Import…' : 'Importer'}
      </button>
      <button type="button" onclick={closeImport}>Fermer</button>
    </div>
  </form>
{/if}

{#if error}<div class="alert">{error}</div>{/if}

{#if loading && methodologies.length === 0}
  <p class="empty">Chargement…</p>
{:else if methodologies.length === 0 && !error}
  <p class="empty">Aucune méthodologie. Créez-en une ou importez un fichier YAML.</p>
{/if}

{#each groups as g (g.name)}
  <section class="card">
    <h3>{g.name}</h3>
    {#if g.description}<p class="desc">{g.description}</p>{/if}
    <ul class="versions">
      {#each g.versions as v (v.version)}
        <li>
          <span class="chip">v{v.version}</span>
          <StatusBadge status={v.status} />
          <span class="when grow">
            {#if v.publishedAt}publiée le {formatDate(v.publishedAt)}{:else if v.updatedAt}modifiée le {formatDate(
                v.updatedAt,
              )}{/if}
            {#if v.goals?.length}· {v.goals.length} objectif{v.goals.length > 1 ? 's' : ''}{/if}
          </span>
          <a class="open" href={href('methodologies', v.name, v.version)}>Ouvrir</a>
        </li>
      {/each}
    </ul>
  </section>
{/each}

<style>
  .head {
    margin-bottom: 0.75rem;
    gap: 0.5rem;
  }
  .head h2 {
    margin: 0;
  }
  .desc {
    color: var(--muted);
    font-size: 0.92rem;
  }
  .versions {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .versions li {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.5rem;
    padding: 0.4rem 0;
    border-top: 1px solid var(--border);
  }
  .versions li:first-child {
    border-top: none;
  }
  .grow {
    flex: 1;
    min-width: 8rem;
  }
  .when {
    font-size: 0.82rem;
    color: var(--muted);
  }
  .open {
    font-weight: 600;
    font-size: 0.88rem;
    padding: 0.2rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
  }
  .open:hover {
    text-decoration: none;
    border-color: var(--accent);
  }
  .check {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    color: var(--text);
    margin-bottom: 0.8rem;
  }
  .check input {
    margin: 0;
  }
  .opt {
    font-weight: 400;
    color: var(--muted);
  }
  .alert.warn {
    background: var(--warn-soft);
    color: var(--warn);
  }
  .issues {
    margin: -0.4rem 0 0.9rem;
    padding-left: 1.2rem;
    font-size: 0.88rem;
  }
  .issues code {
    color: var(--danger);
  }
</style>
