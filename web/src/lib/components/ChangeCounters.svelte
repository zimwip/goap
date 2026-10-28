<script lang="ts">
  // The counters of a change: the nodes its commits wrote (net per node), the properties and links they changed, and
  // the impacts not committed yet.
  import type { ChangeStats } from '../api/types/graph';
  import { counterGroups } from '../changeStats';

  let { stats }: { stats?: ChangeStats } = $props();
  const groups = $derived(counterGroups(stats));
</script>

{#if !groups.empty}
  <div class="counters" aria-label="Counters of the change">
    {#each [{ name: 'Committed', items: groups.nodes }, { name: 'Content', items: groups.content }, { name: 'Pending', items: groups.pending }] as group (group.name)}
      {#if group.items.length}
        <span class="group">
          <span class="name">{group.name}</span>
          {#each group.items as c (c.key)}
            <span class="chip" title={c.title}><b>{c.value}</b> {c.label}</span>
          {/each}
        </span>
      {/if}
    {/each}
  </div>
{/if}

<style>
  .counters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 1rem;
    margin: 0.5rem 0;
  }
  .group {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.35rem;
  }
  .name {
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
  }
  .chip {
    font-size: 0.8rem;
    padding: 0.1rem 0.45rem;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--surface-2);
  }
</style>
