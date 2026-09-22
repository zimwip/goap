<script lang="ts">
  import { registry, errorMessage, formatDate, type Methodology } from '../api';

  let methodologies = $state<Methodology[]>([]);
  let loading = $state(true);
  let error = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      methodologies = (await registry.listMethodologies()).methodologies ?? [];
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });
</script>

<div class="row head">
  <h2 class="grow">Méthodologies</h2>
  <button onclick={load} disabled={loading}>Actualiser</button>
</div>

{#if error}<div class="alert">{error}</div>{/if}

{#if loading && methodologies.length === 0}
  <p class="empty">Chargement…</p>
{:else if methodologies.length === 0 && !error}
  <p class="empty">Aucune méthodologie publiée.</p>
{/if}

{#each methodologies as m (m.name)}
  <section class="card">
    <div class="row">
      <h3 class="grow" style="margin: 0">{m.name}</h3>
      {#if m.version}<span class="chip">v{m.version}</span>{/if}
    </div>
    {#if m.description}<p class="desc">{m.description}</p>{/if}
    {#if m.publishedAt}<p class="hint">Publiée le {formatDate(m.publishedAt)}</p>{/if}
    <h4>Objectifs</h4>
    {#if m.goals?.length}
      <ul class="goals">
        {#each m.goals as g (g.name)}
          <li><code>{g.name}</code>{#if g.description} — {g.description}{/if}</li>
        {/each}
      </ul>
    {:else}
      <p class="empty">Aucun objectif.</p>
    {/if}
  </section>
{/each}

<style>
  .head {
    margin-bottom: 0.5rem;
  }
  .head h2 {
    margin: 0;
  }
  .desc {
    margin-top: 0.5rem;
  }
  .goals {
    margin: 0;
    padding-left: 1.2rem;
  }
  .goals li {
    margin-bottom: 0.25rem;
  }
</style>
