<script lang="ts">
  // The filter of a node's Graph pane: a text box narrowing the nodes shown around the node (key, name, type), and a read
  // query (ADR 0160, a subset of openCypher) that starts from the node (`n`, bound by `bind`) in the view the pane reads:
  // the nodes its rows name are the ones kept, its rows a table below.
  import { graph as graphApi, errorMessage, type GraphNode, type GraphView, type QueryResult } from '../api';
  import { cellOf, matchesFilter, nodeLabel, querySummary, NODE_QUERY_EXAMPLES, valueText } from '../graphQuery';
  import { openNode } from '../nodeEditors';

  let {
    view,
    from,
    fromKey,
    keep = $bindable(),
    onpick,
  }: {
    /** the view the pane reads: the change on its flow, or a baseline of a branch */
    view: GraphView;
    /** the node at the center, the query's `n` */
    from: string;
    fromKey: string;
    /** the nodes the pane shows (unset: every one) */
    keep?: (n: GraphNode) => boolean;
    /** a node of a row picked: the pane centers on it */
    onpick?: (n: GraphNode) => void;
  } = $props();

  let filter = $state('');
  let open = $state(false);
  let text = $state(NODE_QUERY_EXAMPLES[0].query);
  let result = $state<QueryResult | null>(null);
  let error = $state('');
  let running = $state(false);

  // the answer was for the node at the center: another one clears it
  $effect(() => {
    void from;
    result = null;
    error = '';
  });

  $effect(() => {
    const f = filter.trim();
    const answered = result ? new Set((result.nodes ?? []).map((n) => n.id ?? '')) : undefined;
    keep = f || answered ? (n: GraphNode) => matchesFilter(n, f) && (!answered || answered.has(n.id ?? '')) : undefined;
  });

  async function run() {
    if (!text.trim() || !from) return;
    running = true;
    error = '';
    try {
      result = await graphApi.queryView(view, text, { n: from });
    } catch (e) {
      error = errorMessage(e);
    } finally {
      running = false;
    }
  }
  function key(e: KeyboardEvent) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      void run();
    }
  }
</script>

<div class="bar">
  <input type="search" placeholder="Filter the nodes shown (key, name, type)…" bind:value={filter} aria-label="Filter the nodes shown" />
  <button type="button" class="small" class:primary={open} aria-pressed={open} onclick={() => (open = !open)}>Query</button>
  {#if result}<button type="button" class="small" onclick={() => (result = null)}>Clear result</button>{/if}
</div>
{#if open}
  <div class="query">
    <textarea
      class="mono"
      rows="3"
      spellcheck="false"
      bind:value={text}
      onkeydown={key}
      aria-label="Query (a subset of openCypher)"
      placeholder="MATCH (n)-[l]-(m) WHERE m.title CONTAINS 'pay' RETURN m"
    ></textarea>
    <div class="side">
      <span class="hint">From <span class="mono">n</span> = {fromKey}</span>
      <select
        aria-label="Examples"
        onchange={(e) => {
          const s = e.currentTarget as HTMLSelectElement;
          if (s.value) text = s.value;
          s.selectedIndex = 0;
        }}
      >
        <option value="">Examples…</option>
        {#each NODE_QUERY_EXAMPLES as x (x.label)}<option value={x.query}>{x.label}</option>{/each}
      </select>
      <button type="button" class="small primary" onclick={() => void run()} disabled={running || !text.trim()}>{running ? 'Running…' : 'Run'}</button>
      <span class="hint" title="Read-only MATCH, OPTIONAL MATCH, WHERE (EXISTS, patterns), RETURN; hops and rows are capped (ADR 0160)">Ctrl+Enter runs it</span>
    </div>
  </div>
  {#if error}<div class="alert mono">{error}</div>{/if}
{/if}
{#if result}
  <div class="results">
    <p class="hint">
      {querySummary(result)}: the graph shows the nodes the rows name
      {#each result.notes ?? [] as n (n)}<br />{n}{/each}
    </p>
    {#if result.rows?.length}
      <table>
        <thead><tr>{#each result.columns ?? [] as c, i (i)}<th class="mono">{c}</th>{/each}</tr></thead>
        <tbody>
          {#each result.rows as row, r (r)}
            <tr>
              {#each row as v, i (i)}
                {@const c = cellOf(v, result)}
                <td>
                  {#if c.kind === 'node' && c.node}
                    {@const n = c.node}
                    <button type="button" class="link" onclick={() => onpick?.(n)} ondblclick={() => void openNode(n)} title="{n.key} · {n.type}">{nodeLabel(n, c.id)}</button>
                  {:else}
                    <span class="mono">{valueText(v, result)}</span>
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="hint">No row.</p>
    {/if}
  </div>
{/if}

<style>
  .bar {
    display: flex;
    gap: 8px;
    align-items: center;
    margin-bottom: 6px;
  }
  .bar input {
    flex: 1;
    min-width: 160px;
  }
  .query {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 6px;
  }
  .query textarea {
    flex: 1;
    min-width: 260px;
    resize: vertical;
    font-size: 0.9em;
  }
  .side {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 180px;
    align-items: flex-start;
  }
  .results {
    max-height: 24vh;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 6px 8px;
    margin-bottom: 6px;
  }
  .results table {
    border-collapse: collapse;
    width: 100%;
    font-size: 0.9em;
  }
  .results th,
  .results td {
    text-align: left;
    padding: 2px 8px 2px 0;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
</style>
