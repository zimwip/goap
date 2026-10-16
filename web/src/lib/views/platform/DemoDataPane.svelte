<script lang="ts">
  // "Demo data" section of the settings modal (administrators): the demo is not loaded at start, an administrator
  // loads it here on demand (GraphService.LoadDemoData). It writes ordinary changes, so it is applied at once and
  // nothing goes through the Save bar.
  import { graph, errorMessage } from '../../api';
  import { project, refreshProjects, selectProject } from '../../stores/project.svelte';

  let busy = $state(false);
  let error = $state('');
  let result = $state<{ loaded: boolean; project: string } | null>(null);
  const demoProject = $derived(result?.project ?? '');
  const inDemo = $derived(!!demoProject && project.current === demoProject);

  async function load() {
    busy = true;
    error = '';
    try {
      const r = await graph.loadDemoData();
      result = { loaded: !!r.loaded, project: r.project ?? '' };
      await refreshProjects();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="demo">
  {#if error}<div class="alert">{error}</div>{/if}
  <section class="card">
    <h3>Load demo data</h3>
    <p class="hint">
      Loads a small example repository: the <strong>Acme</strong> organisation and its teams, the
      <strong>Online commerce</strong> demo project bound to it (Acme holds the roles of the SDLC methodology there), and
      the needs, requirements, components, applications and releases of that project. Only what is missing is written,
      so loading again changes nothing.
    </p>
    <div class="row">
      <button type="button" class="small primary" disabled={busy} onclick={load}>{busy ? 'Loading…' : 'Load demo data'}</button>
      {#if result && demoProject && !inDemo}
        <button type="button" class="small" onclick={() => selectProject(demoProject)}>Switch to the demo project</button>
      {/if}
    </div>
    {#if result}
      <p class="hint done">{result.loaded ? 'The demo data is loaded.' : 'The demo data was already there: nothing was written.'}</p>
    {/if}
  </section>
</div>

<style>
  .demo {
    display: grid;
    gap: 0.8rem;
  }
  h3 {
    margin: 0 0 0.5rem;
    font-size: 0.95em;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
  }
  .done {
    margin: 0.5rem 0 0;
  }
</style>
