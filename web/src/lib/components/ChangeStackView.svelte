<script lang="ts">
  // The change-stack of a node version or a baseline (ADR 0133): the changes it is the application of, oldest first,
  // a sub-change under its parent, each with what it did in the lineage. A row opens its change.
  import { graph, errorMessage, type ChangeStack } from '../api';
  import { stackRows } from '../changeStack';
  import { openTab } from '../shell/tabs.svelte';

  let { nodeId, version, baselineId }: { nodeId?: string; version?: number; baselineId?: string } = $props();

  let stack = $state<ChangeStack | undefined>();
  let error = $state('');

  $effect(() => {
    const q = baselineId ? { baselineId } : { nodeId, version };
    stack = undefined;
    error = '';
    if (!q.baselineId && !q.nodeId) return;
    const ctrl = new AbortController();
    graph
      .getChangeStack(q, ctrl.signal)
      .then((r) => (stack = r.stack ?? {}))
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      });
    return () => ctrl.abort();
  });

  const rows = $derived(stackRows(stack));
</script>

<div class="stack">
  {#if error}<div class="alert">{error}</div>{/if}
  {#if stack}
    <p class="hint">
      {rows.length} change{rows.length === 1 ? '' : 's'} produce{rows.length === 1 ? 's' : ''} {baselineId ? 'this baseline' : `v${version ?? ''}`}{stack.truncated ? ' (oldest ones left out)' : ''}.
    </p>
    <ol aria-label="Change-stack">
      {#each rows as r (r.id)}
        <li style:padding-left="{r.depth * 1.2}rem">
          <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: r.id } }, { pin: true })}>{r.label}</button>
          {#if r.parent}<span class="hint">sub-change</span>{/if}
          {#if r.status}<span class="status">{r.status}</span>{/if}
          {#if r.how.length}<span class="hint">· {r.how.join(' · ')}</span>{/if}
        </li>
      {/each}
    </ol>
  {:else if !error}
    <p class="empty">Loading…</p>
  {/if}
</div>

<style>
  ol {
    list-style: none;
    padding: 0;
    margin: 0.3rem 0 0;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    align-items: baseline;
    padding: 0.15rem 0;
  }
  .status {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.8rem;
  }
</style>
