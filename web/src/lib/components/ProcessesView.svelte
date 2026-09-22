<script lang="ts">
  import { engine, errorMessage, formatDate, shortId, type Process } from '../api';
  import { nav, go, href } from '../nav.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import StartProcessForm from './StartProcessForm.svelte';
  import ProcessDetail from './ProcessDetail.svelte';

  let processes = $state<Process[]>([]);
  let loading = $state(false);
  let error = $state('');

  const selected = $derived(nav.id);

  async function load() {
    loading = true;
    try {
      const list = (await engine.listProcesses()).processes ?? [];
      processes = list.sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  function started(p: Process) {
    processes = [p, ...processes];
    go('processus', p.id ?? '');
  }

  // Répercute les mises à jour du détail dans la liste.
  function updated(p: Process) {
    const i = processes.findIndex((x) => x.id === p.id);
    if (i >= 0 && processes[i].status !== p.status) processes[i] = { ...processes[i], status: p.status, goal: p.goal };
  }
</script>

<div class="layout">
  <aside class="list">
    <div class="row head">
      <h2 class="grow">Processus</h2>
      <button class="small" onclick={load} disabled={loading}>Actualiser</button>
    </div>
    <a class="new" class:active={!selected} href={href('processus')}>+ Nouveau processus</a>
    {#if error}<div class="alert">{error}</div>{/if}
    <ul>
      {#each processes as p (p.id)}
        <li>
          <a href={href('processus', p.id)} class:active={p.id === selected}>
            <div class="row">
              <code class="grow">{shortId(p.id)}</code>
              <StatusBadge status={p.status} />
            </div>
            <div class="sub">{p.methodology}{p.goal ? ` · ${p.goal}` : ''}</div>
            {#if p.createdAt}<div class="sub">{formatDate(p.createdAt)}</div>{/if}
          </a>
        </li>
      {:else}
        {#if !loading && !error}<li class="empty">Aucun processus.</li>{/if}
      {/each}
    </ul>
  </aside>

  <div class="detail">
    {#if selected}
      {#key selected}
        <ProcessDetail id={selected} onupdate={updated} />
      {/key}
    {:else}
      <StartProcessForm onstarted={started} />
    {/if}
  </div>
</div>

<style>
  .layout {
    display: grid;
    grid-template-columns: 270px minmax(0, 1fr);
    gap: 1.5rem;
    align-items: start;
  }
  .head h2 {
    margin: 0;
  }
  .head {
    margin-bottom: 0.75rem;
  }
  .new {
    display: block;
    padding: 0.5rem 0.7rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius-sm);
    margin-bottom: 0.75rem;
    font-weight: 600;
    font-size: 0.9rem;
  }
  .new.active {
    border-style: solid;
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.35rem;
  }
  li a {
    display: block;
    padding: 0.5rem 0.7rem;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
  }
  li a:hover {
    text-decoration: none;
    border-color: var(--accent);
  }
  li a.active {
    border-color: var(--accent);
    box-shadow: 0 0 0 2px var(--accent-soft);
  }
  .sub {
    font-size: 0.8rem;
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  @media (max-width: 900px) {
    .layout {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
