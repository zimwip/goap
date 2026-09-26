<script lang="ts">
  // The domain (graph) branch of a flow: chip, competition, and a review of what it changes.
  import { errorMessage, graph, type Flow, type MergePlan } from '../api';

  let { flow, changeBranch }: { flow: Flow; changeBranch: string } = $props();

  let plan = $state<MergePlan | undefined>();
  let loading = $state(false);
  let error = $state('');
  let opened = $state(false);

  async function review() {
    if (!flow.branch) return;
    loading = true;
    error = '';
    try {
      plan = (await graph.planMerge(flow.branch, changeBranch || 'main')).plan;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }
  function toggle() {
    opened = !opened;
    if (opened && !plan && !loading) void review();
  }
</script>

<div class="branch-info">
  {#if flow.competesWith?.length}
    <span class="chip competing" title="Adopting is refused: discard this flow or relaunch the step">
      competes with {flow.competesWith.map((c) => c.slice(0, 8)).join(', ')}
    </span>
  {/if}
  {#if flow.branch}
    <span class="chip" title="graph branch holding the proposals of this flow"><code>{flow.branch}</code></span>
    {#if flow.merged?.length}
      <span class="hint">merged into <code>{changeBranch || 'main'}</code></span>
    {:else if flow.status === 'open'}
      <button type="button" class="link" onclick={toggle}>{opened ? 'Hide' : 'Review'} graph changes</button>
    {/if}
  {:else}
    <span class="hint">no graph preview (no proposals)</span>
  {/if}
  {#if opened}
    {#if loading}<p class="hint">Loading…</p>{/if}
    {#if error}<p class="alert">{error}</p>{/if}
    {#if plan}
      {#if plan.candidates?.length}
        <table class="candidates">
          <thead><tr><th>Node</th><th>Type</th><th>Change</th><th>Conflicts</th></tr></thead>
          <tbody>
            {#each plan.candidates as c (c.node)}
              <tr class:conflict={c.conflicts?.length}>
                <td><code>{c.key}</code></td>
                <td>{c.type}</td>
                <td>{c.deleted ? 'deleted' : (c.kind ?? '').replace('_', ' ')}</td>
                <td>{c.conflicts?.join(', ') ?? ''}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}
        <p class="hint">The branch changes nothing compared with <code>{changeBranch || 'main'}</code>.</p>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .branch-info {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .chip {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 8px;
    font-size: 0.85em;
  }
  .competing {
    border-color: var(--warn);
    color: var(--warn);
  }
  .candidates {
    width: 100%;
    font-size: 0.85em;
    border-collapse: collapse;
  }
  .candidates th,
  .candidates td {
    text-align: left;
    padding: 2px 8px;
    border-bottom: 1px solid var(--border);
  }
  .conflict {
    color: var(--danger);
  }
</style>
