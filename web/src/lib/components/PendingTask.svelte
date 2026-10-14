<script lang="ts">
  // The panel answering what a run waits for from a person (its pending task): the same in the run tab and in the
  // asks of the change (ADR 0117).
  import { shortId, type Flow, type Process } from '../api';
  import { processes } from '../stores/live.svelte';
  import { restartedStepNumber } from '../flowChain';
  import StatusBadge from './StatusBadge.svelte';
  import HumanTaskForm from './HumanTaskForm.svelte';
  import ApprovalPanel from './ApprovalPanel.svelte';
  import UnblockPanel from './UnblockPanel.svelte';
  import FlowDecisionPanel from './FlowDecisionPanel.svelte';
  import BoardIssuesPanel from './BoardIssuesPanel.svelte';
  import MergeTaskPanel from './MergeTaskPanel.svelte';
  import RebaseTaskPanel from './RebaseTaskPanel.svelte';

  let {
    process,
    flows = [],
    ondecided,
    onopen,
  }: {
    process: Process;
    flows?: Flow[];
    ondecided: (p: Process) => void;
    /** opens a run (pin: keep its tab) */
    onopen: (processId: string, pin?: boolean) => void;
  } = $props();
</script>

{#if process.pending}
  {#key `${process.id}:${process.pending.step}:${process.pending.action}:${process.pending.kind}`}
    {#if process.pending.kind === 'approval'}
      <ApprovalPanel {process} {ondecided} />
    {:else if process.pending.kind === 'flow'}
      <FlowDecisionPanel {process} step={restartedStepNumber(process, processes)} {ondecided} />
    {:else if process.pending.kind === 'board'}
      <BoardIssuesPanel {process} {flows} {ondecided} onopen={(pid) => onopen(pid)} />
    {:else if process.pending.kind === 'merge'}
      <MergeTaskPanel {process} {ondecided} />
    {:else if process.pending.kind === 'rebase'}
      <RebaseTaskPanel {process} {ondecided} />
    {:else if process.pending.kind === 'relaunched'}
      {@const awaited = [...processes.values()].find((q) => q.flow === process.pending?.flowId)}
      <section class="card waiting-agent">
        <h3>Waiting for the decision on the relaunched flow</h3>
        <p>
          This run restarts when the flow relaunched to fix its blackboard is adopted or discarded
          {#if awaited?.id}: <button type="button" class="link mono" onclick={() => onopen(awaited?.id ?? '')}>{awaited.agent || shortId(awaited.id)}</button>{/if}.
        </p>
        {#if process.pending.description}<p class="hint">{process.pending.description}</p>{/if}
      </section>
    {:else if process.pending.kind === 'condition' || process.pending.kind === 'unblock'}
      <UnblockPanel {process} {ondecided} />
    {:else if process.pending.kind === 'agent'}
      <section class="card waiting-agent">
        <h3>Waiting for a sub-agent</h3>
        <p>
          Action <code>{process.pending.action}</code> is waiting for the sub-agent
          {#if process.pending.childProcessId}
            <button type="button" class="link mono" onclick={() => onopen(process.pending?.childProcessId ?? '', true)}>
              {processes.get(process.pending.childProcessId)?.agent || shortId(process.pending.childProcessId)}
            </button>
            {#if processes.get(process.pending.childProcessId)}
              <StatusBadge status={processes.get(process.pending.childProcessId)?.status} />
            {/if}
          {/if} to finish. It will be replayed once that completes.
        </p>
        {#if process.pending.description}<p class="hint">{process.pending.description}</p>{/if}
      </section>
    {:else}
      <HumanTaskForm {process} onsubmitted={ondecided} />
    {/if}
  {/key}
{/if}

<style>
  .waiting-agent {
    border-left: 3px solid var(--warn);
  }
</style>
