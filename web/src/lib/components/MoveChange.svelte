<script lang="ts">
  // Moves a change to another project (ADR 0091): a root change, draft or active, with its sub-changes. The projects
  // offered are those whose methodologies include the change's; the server stays the authority (the caller's access to
  // both projects, the methodology rule). The nodes the change already acts on keep their project; the ones it creates
  // take the new one when it lands.
  import { graph, errorMessage, type Change } from '../api';
  import { headGraph } from '../graphEdit';
  import { applicableMethodologies } from '../projectRoles';
  import { moveChoices, type MoveChoices } from '../changeProject';
  import { project, refreshProjects, selectProject } from '../stores/project.svelte';
  import { ns, rootProject } from '../stores/session.svelte';
  import { refreshChanges } from '../stores/catalog.svelte';
  import { notify } from '../shell/workbench.svelte';

  let { change, onmoved, oncancel }: { change: Change; onmoved: () => void; oncancel: () => void } = $props();

  let offer = $state<MoveChoices | undefined>();
  let target = $state('');
  let busy = $state(false);
  let error = $state('');

  const current = $derived(change.projectId || rootProject());

  $effect(() => {
    const ch = change;
    void (async () => {
      try {
        if (!project.options.length) await refreshProjects();
        const head = await headGraph(ns.organisation);
        offer = moveChoices({
          methodology: ch.methodology,
          current: ch.projectId || rootProject(),
          projects: project.options,
          applicable: (key) => applicableMethodologies(head, key),
        });
        if (!offer.choices.some((c) => c.key === target)) target = offer.choices[0]?.key ?? '';
      } catch (e) {
        error = errorMessage(e);
      }
    })();
  });

  async function move(e: SubmitEvent) {
    e.preventDefault();
    if (!change.id || !target) return;
    busy = true;
    error = '';
    try {
      await graph.moveChange(change.id, target);
      // the active project follows the change, the catalog is read again (the explorer filters by project)
      await selectProject(target);
      await refreshChanges();
      notify(`Change moved to project ${target}.`, 'ok');
      onmoved();
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<form class="move" onsubmit={move}>
  {#if !offer}
    <span class="muted">Reading the projects…</span>
  {:else if offer.blocked}
    <span class="muted">{offer.blocked}</span>
    <button type="button" class="small" onclick={oncancel}>Close</button>
  {:else}
    <label for="mv-project">Move to project</label>
    <select id="mv-project" bind:value={target}>
      {#each offer.choices as c (c.key)}<option value={c.key}>{c.label}</option>{/each}
    </select>
    <p class="hint">
      The change leaves {current} with its sub-changes. The nodes it already acts on keep their project; the nodes it creates will belong to
      {target || 'the new project'}. Only projects applying the methodology of the change are offered.
    </p>
    <div class="row">
      <button type="submit" class="primary small" disabled={busy || !target}>Move</button>
      <button type="button" class="small" onclick={oncancel}>Cancel</button>
    </div>
  {/if}
  {#if error}<div class="alert small">{error}</div>{/if}
</form>

<style>
  .move {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 0.4rem 0;
  }
  label {
    font-size: 0.8rem;
    color: var(--muted);
  }
  .row {
    display: flex;
    gap: 6px;
  }
  .hint {
    margin: 0;
    font-size: 0.85em;
    color: var(--muted);
  }
  .alert.small {
    font-size: 0.88em;
  }
</style>
