<script lang="ts">
  // Where the run of an agent the assistant started stands (ADR 0090): read from the process the engine keeps (the
  // live stream updates it, nothing is mirrored by the assistant), with links to the change and to the run.
  import { engine, type Process } from '../../api';
  import { runStatus } from '../../assistant/proposal';
  import { ingestProcess, processes } from '../../stores/live.svelte';
  import { changes } from '../../stores/catalog.svelte';
  import { followChangeProject } from '../../stores/project.svelte';
  import { openTab } from '../../shell/tabs.svelte';

  let { processId, changeId = '' }: { processId: string; changeId?: string } = $props();

  const process = $derived<Process | undefined>(processes.get(processId));
  const status = $derived(runStatus(process));
  const change = $derived(changeId || process?.changeId || '');

  // a process the stream has not told about yet is read once
  $effect(() => {
    const id = processId;
    if (!id || processes.has(id)) return;
    const ctl = new AbortController();
    engine
      .getProcess(id, ctl.signal)
      .then((r) => ingestProcess(r.process))
      .catch(() => {});
    return () => ctl.abort();
  });

  async function openChange() {
    const known = changes.items.find((c) => c.id === change);
    await followChangeProject({ id: change, projectId: known?.projectId });
    openTab({ kind: 'change', params: { id: change } }, { pin: true });
  }
</script>

<div class="run" role="status" aria-live="polite">
  <span class="badge {status.tone}">{status.label}</span>
  {#if change}<button type="button" class="link" onclick={openChange}>Open the change</button>{/if}
  {#if processId}<button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: processId } }, { pin: true })}>Open the run</button>{/if}
</div>

<style>
  .run {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.35rem;
  }
  .badge {
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.85em;
    background: var(--neutral-soft);
    color: var(--muted);
  }
  .badge.ok {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .badge.warn {
    background: var(--warn-soft);
    color: var(--warn);
  }
  .badge.err {
    background: var(--danger-soft);
    color: var(--danger);
  }
</style>
