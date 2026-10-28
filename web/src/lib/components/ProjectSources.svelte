<script lang="ts">
  // The sources a project reads its logical URLs through (ADR 0154 §8): the name a URL gives a source mapped to a
  // source declared on the platform (Settings → Sources). The mapping is inherited by the sub-projects, the nearest
  // project wins; a name mapped nowhere is the namespace of that name of this graph.
  import { errorMessage, type GraphNode } from '../api';
  import { applyOnMain, headGraph, updateNodeItem, type HeadGraph } from '../graphEdit';
  import { ns, types as nodeTypes, links as linkTypes } from '../stores/session.svelte';
  import { notify } from '../shell/workbench.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { openSettings } from '../shell/settingsState.svelte';
  import { SOURCE_NAME_RE, SOURCE_TYPE, ownSources, projectParents, sourceOf, sourcesInForce, type Source } from '../sources';

  let { head, project, onChanged }: { head: HeadGraph; project: GraphNode; onChanged: () => Promise<void> | void } = $props();

  const key = $derived(project.key ?? '');
  const parents = $derived(projectParents(head.nodes, head.links, linkTypes.projectPartOf));
  const propsOf = (k: string) => head.nodes.find((n) => n.type === nodeTypes.projectUnit && n.key === k)?.props;
  const inForce = $derived(sourcesInForce(key, propsOf, (k) => parents.get(k) ?? ''));

  // the sources the platform declares, to choose from (an administrator's reading; others type the name)
  let declared = $state<Source[]>([]);
  $effect(() => {
    void headGraph(ns.platform)
      .then((g) => (declared = g.nodes.filter((n) => n.type === SOURCE_TYPE).flatMap((n) => sourceOf(n.key ?? '', n.props) ?? [])))
      .catch(() => (declared = []));
  });
  const declaredNames = $derived(new Set(declared.map((s) => s.name)));

  let editing = $state(false);
  let rows = $state<{ name: string; source: string }[]>([]);
  let saving = $state(false);
  let error = $state('');

  function startEdit() {
    rows = Object.entries(ownSources(project.props)).map(([name, source]) => ({ name, source }));
    if (!rows.length) rows = [{ name: '', source: '' }];
    error = '';
    editing = true;
  }

  const problem = $derived.by(() => {
    const names = new Set<string>();
    for (const r of rows) {
      if (!r.name && !r.source) continue;
      if (!SOURCE_NAME_RE.test(r.name)) return `"${r.name}" is no source name: a lowercase letter, then lowercase letters, digits, ".", "_" or "-".`;
      if (!SOURCE_NAME_RE.test(r.source)) return `${r.name}: name the declared source holding it.`;
      if (names.has(r.name)) return `${r.name} is mapped twice.`;
      names.add(r.name);
    }
    return '';
  });

  async function save() {
    if (problem) return;
    saving = true;
    error = '';
    try {
      const map: Record<string, string> = {};
      for (const r of rows) if (r.name) map[r.name] = r.source;
      await applyOnMain(ns.organisation, `Sources of project ${key}`, `Map the sources of the project ${key}`, head.commitId, [
        updateNodeItem(project, { sources: Object.keys(map).length ? map : null }),
      ]);
      notify(`Sources of ${key} updated.`, 'ok');
      editing = false;
      await onChanged();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }
</script>

<section class="card">
  <h3>Sources</h3>
  {#if error}<div class="alert">{error}</div>{/if}
  <p class="hint">
    A link may point to <code>lnk:&lt;source&gt;/&lt;type&gt;/&lt;key&gt;</code>. Here the project says which source declared on the platform holds each source its URLs
    name (its sub-projects inherit it, the nearest project wins). A name mapped nowhere is the namespace of that name of this graph.
    <button type="button" class="link" onclick={() => openSettings('sources')}>Declared sources…</button>
  </p>
  {#if !editing}
    {#if inForce.length}
      <table class="tbl">
        <thead><tr><th>Name in the URLs</th><th>Declared source</th><th>Set on</th></tr></thead>
        <tbody>
          {#each inForce as e (e.name)}
            <tr>
              <td><code>{e.name}</code></td>
              <td><code>{e.source}</code>{#if declared.length && !declaredNames.has(e.source)} <span class="tag warn" title="No source of this name is declared on the platform">not declared</span>{/if}</td>
              <td>
                {#if e.from === key}<span class="muted">this project</span>{:else}<button type="button" class="link mono" onclick={() => openTab({ kind: 'project', params: { key: e.from } })}>{e.from}</button>{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="muted">No mapping: every URL reads a namespace of this graph.</p>
    {/if}
    <button type="button" class="small" onclick={startEdit}>Edit sources</button>
  {:else}
    <table class="tbl">
      <thead><tr><th>Name in the URLs</th><th>Declared source</th><th></th></tr></thead>
      <tbody>
        {#each rows as r, i (i)}
          <tr>
            <td><input class="mono" bind:value={r.name} spellcheck="false" placeholder="alm" aria-label="Name in the URLs" /></td>
            <td>
              <input class="mono" list="project-sources-declared" bind:value={r.source} spellcheck="false" placeholder="acme-alm" aria-label="Declared source" />
            </td>
            <td><button type="button" class="small" onclick={() => (rows = rows.filter((_, j) => j !== i))}>Remove</button></td>
          </tr>
        {/each}
      </tbody>
    </table>
    <datalist id="project-sources-declared">{#each declared as s (s.name)}<option value={s.name}>{s.description}</option>{/each}</datalist>
    {#if problem}<p class="hint warn">{problem}</p>{/if}
    <div class="row">
      <button type="button" class="small" onclick={() => (rows = [...rows, { name: '', source: '' }])}>Add a mapping</button>
      <button type="button" class="small primary" disabled={saving || !!problem} onclick={save}>Save</button>
      <button type="button" class="small" onclick={() => (editing = false)}>Cancel</button>
    </div>
  {/if}
</section>

<style>
  .tbl {
    width: 100%;
    border-collapse: collapse;
    margin-bottom: 0.5rem;
  }
  .tbl th,
  .tbl td {
    text-align: left;
    padding: 0.25rem 0.5rem;
    border-bottom: 1px solid var(--border, #8884);
    vertical-align: top;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .warn {
    color: var(--warn);
  }
</style>
