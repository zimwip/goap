<script lang="ts">
  import { engine, errorMessage, shortId, type Process } from '../api';
  import { processes, ingestProcess } from '../stores/live.svelte';
  import BoardIssueList from './BoardIssueList.svelte';
  import FlowGraph from './FlowGraph.svelte';

  let {
    process,
    flows = [],
    ondecided,
    onopen,
  }: {
    process: Process;
    flows?: import('../api').Flow[];
    ondecided: (p: Process) => void;
    onopen: (processId: string) => void;
  } = $props();

  const issues = $derived(process.pending?.issues ?? []);
  const proposal = $derived(process.pending?.proposal);

  let comment = $state('');
  let busy = $state(false);
  let error = $state('');

  async function resolve(relaunch: boolean) {
    if (!process.id) return;
    busy = true;
    error = '';
    try {
      const res = await engine.resolveBoard(process.id, relaunch, comment.trim());
      if (res.relaunched) ingestProcess(res.relaunched);
      if (res.process) ondecided(res.process);
      if (res.relaunched?.id) onopen(res.relaunched.id);
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<section class="card board-issues">
  <h3>The blackboard is inconsistent — {issues.length} issue{issues.length === 1 ? '' : 's'}</h3>
  <BoardIssueList {issues} />
  {#if proposal}
    <div class="proposal">
      <p>
        <strong>Restart from step {(proposal.step ?? 0) + 1}</strong> (<code>{proposal.action}</code>) of process
        <button type="button" class="link mono" onclick={() => onopen(proposal.process ?? '')}>
          {processes.get(proposal.process ?? '')?.agent || shortId(proposal.process ?? '')}
        </button>
        , the earliest step that produced faulty content.
      </p>
      {#if proposal.reason}<p class="hint">{proposal.reason}</p>{/if}
      {#if proposal.culprits?.length}
        <p class="hint">Faulty items: {#each proposal.culprits as c, k (c)}{k ? ', ' : ''}<code>{shortId(c)}</code>{/each}</p>
      {/if}
      <details>
        <summary>Show in the flow</summary>
        <FlowGraph {processes} processId={proposal.process ?? ''} {flows} {proposal} {onopen} />
      </details>
    </div>
  {:else}
    <p class="hint">
      No step can be relaunched to fix this: the faulty content comes from a human or a trigger.
    </p>
  {/if}
  <label class="field">
    <span>Guidance for the agent (optional)</span>
    <textarea rows="2" placeholder="What should the relaunched steps take into account?" bind:value={comment}></textarea>
  </label>
  <p class="hint">On restart it is recorded on the new branch and added to the prompts of the relaunched steps; on ignore it is only kept in the journal.</p>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="row">
    {#if proposal}<button class="primary" disabled={busy} onclick={() => resolve(true)}>Restart from here</button>{/if}
    <button disabled={busy} onclick={() => resolve(false)}>Ignore and continue</button>
  </div>
</section>

<style>
  .board-issues {
    border-left: 3px solid var(--warn);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 8px 0;
  }
  .hint {
    color: var(--muted);
  }
</style>
