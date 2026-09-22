<script lang="ts">
  import { nav, TABS, href } from './lib/nav.svelte';
  import { getToken, setToken } from './lib/api';
  import ProcessesView from './lib/components/ProcessesView.svelte';
  import ChangeView from './lib/components/ChangeView.svelte';
  import BaselineView from './lib/components/BaselineView.svelte';
  import MethodologiesView from './lib/components/MethodologiesView.svelte';

  let token = $state(getToken() ?? '');
  let tokenOpen = $state(false);

  function saveToken(e: SubmitEvent) {
    e.preventDefault();
    setToken(token.trim() || null);
    tokenOpen = false;
  }
</script>

<div class="shell">
  <aside>
    <div class="brand">GOAP</div>
    <nav>
      {#each TABS as t (t.id)}
        <a href={href(t.id)} class:active={nav.tab === t.id} aria-current={nav.tab === t.id ? 'page' : undefined}>
          {t.label}
        </a>
      {/each}
    </nav>
    <div class="token">
      {#if tokenOpen}
        <form onsubmit={saveToken}>
          <label for="token">Jeton d'accès</label>
          <input id="token" type="password" bind:value={token} placeholder="Bearer…" autocomplete="off" />
          <div class="row" style="margin-top: 0.4rem">
            <button class="small primary" type="submit">Enregistrer</button>
            <button class="small" type="button" onclick={() => (tokenOpen = false)}>Annuler</button>
          </div>
        </form>
      {:else}
        <button class="small linkish" onclick={() => (tokenOpen = true)}>
          {getToken() ? 'Jeton configuré' : 'Configurer un jeton'}
        </button>
      {/if}
    </div>
  </aside>

  <main>
    {#if nav.tab === 'processus'}
      <ProcessesView />
    {:else if nav.tab === 'changement'}
      <ChangeView />
    {:else if nav.tab === 'referentiel'}
      <BaselineView />
    {:else}
      <MethodologiesView />
    {/if}
  </main>
</div>

<style>
  .shell {
    display: grid;
    grid-template-columns: 210px 1fr;
    min-height: 100vh;
  }
  aside {
    background: var(--surface);
    border-right: 1px solid var(--border);
    padding: 1.1rem 0.8rem;
    display: flex;
    flex-direction: column;
    gap: 1.2rem;
    position: sticky;
    top: 0;
    height: 100vh;
  }
  .brand {
    font-weight: 800;
    letter-spacing: 0.08em;
    padding: 0 0.6rem;
    font-size: 1.1rem;
  }
  nav {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  nav a {
    color: var(--text);
    padding: 0.45rem 0.6rem;
    border-radius: var(--radius-sm);
    font-weight: 500;
  }
  nav a:hover {
    background: var(--surface-2);
    text-decoration: none;
  }
  nav a.active {
    background: var(--accent-soft);
    color: var(--accent);
    font-weight: 600;
  }
  .token {
    margin-top: auto;
    padding: 0 0.3rem;
  }
  .linkish {
    border: none;
    background: none;
    color: var(--muted);
    font-weight: 500;
    padding: 0.2rem 0.3rem;
  }
  main {
    padding: 1.6rem 2rem 3rem;
    max-width: 1200px;
    width: 100%;
    min-width: 0;
  }

  @media (max-width: 760px) {
    .shell {
      grid-template-columns: 1fr;
    }
    aside {
      position: static;
      height: auto;
      flex-direction: row;
      align-items: center;
      flex-wrap: wrap;
      border-right: none;
      border-bottom: 1px solid var(--border);
      padding: 0.6rem 1rem;
    }
    nav {
      flex-direction: row;
      flex-wrap: wrap;
    }
    .token {
      margin-top: 0;
      margin-left: auto;
    }
    main {
      padding: 1rem;
    }
  }
</style>
