<script lang="ts">
  // "Graph" tool: the way into the Graph explorer (ADR 0036 §4, ADR 0160): open it, start from the organisation or
  // from a node found by its key, name or type, or run a query, an example to start from or the one written here.
  import Icon from '../../shell/Icon.svelte';
  import { QUERY_EXAMPLES } from '../../graphQuery';
  import { askGraphExplorer } from '../../stores/graphExplorer.svelte';

  let find = $state('');

  function search(e: SubmitEvent) {
    e.preventDefault();
    if (find.trim()) askGraphExplorer({ kind: 'find', text: find.trim() });
  }
</script>

<div class="explorer">
  <div class="actions">
    <button type="button" class="small primary" onclick={() => askGraphExplorer()}><Icon name="network" size={14} /> Open the Graph explorer</button>
    <button type="button" class="small" onclick={() => askGraphExplorer({ kind: 'organisation' })}>Start from the organisation</button>
    <button type="button" class="small" onclick={() => askGraphExplorer({ kind: 'query', text: '', run: false })}><Icon name="search" size={14} /> Write a query</button>
  </div>
  <form class="find" onsubmit={search}>
    <input type="search" placeholder="Start from a node (key, name, type)…" bind:value={find} aria-label="Start from a node" />
  </form>
  <div class="ui-section-head lead">Example queries</div>
  <ul class="list" aria-label="Example queries">
    {#each QUERY_EXAMPLES as ex (ex.label)}
      <li>
        <button type="button" class="ex" title={ex.query} onclick={() => askGraphExplorer({ kind: 'query', text: ex.query, run: true })}>
          <span class="name">{ex.label}</span>
          <code>{ex.query.split('\n')[0]}</code>
        </button>
      </li>
    {/each}
  </ul>
</div>

<style>
  .actions {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 0 0.8rem 0.5rem;
  }
  .actions button {
    justify-content: flex-start;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .find {
    padding: 0 0.8rem 0.6rem;
  }
  .find input {
    width: 100%;
  }
  .lead {
    padding: 0.2rem 0.8rem;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .ex {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    width: 100%;
    border: none;
    border-radius: 0;
    background: transparent;
    padding: 0.35rem 0.8rem;
    text-align: left;
  }
  .ex:hover {
    background: var(--hover);
  }
  .ex code {
    font-size: 11px;
    color: var(--text-3);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }
</style>
