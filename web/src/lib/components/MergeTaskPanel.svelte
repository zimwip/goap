<script lang="ts">
  // A committed change whose merge found nodes both branches changed apart (engine task "merge"): shows the value of
  // each side, proposes the change's, and completes the merge with the resolutions, which resumes the run. The rest of
  // the merge (what changed on one side only, or different properties) was done by the three-way merge itself.
  import { engine, type Process, type Resolution } from '../api';
  import MergeResolver from './MergeResolver.svelte';

  let { process, ondecided }: { process: Process; ondecided: (p: Process) => void } = $props();

  const task = $derived(process.pending?.merge);

  async function merge(resolutions: Record<string, Resolution>): Promise<boolean> {
    if (!process.id) return false;
    const res = await engine.resolveMerge(process.id, resolutions);
    if (res.process) ondecided(res.process);
    return true;
  }
</script>

{#if task}
  <section class="card merge-task">
    <h3>The merge of the change waits for your decision — {task.conflicts?.length ?? 0} node{task.conflicts?.length === 1 ? '' : 's'}</h3>
    {#if process.pending?.description}<p>{process.pending.description}</p>{/if}
    <p class="hint">Where the target branch and the change set the same property differently, the change's value is proposed; pick the other one to keep it.</p>
    <MergeResolver namespace={task.namespace ?? ''} from={task.from ?? ''} into={task.into ?? ''} submitLabel="Merge and go on" onmerge={merge} />
  </section>
{/if}
