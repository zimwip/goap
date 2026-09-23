<script lang="ts">
  let {
    world = {},
    unknown = {},
  }: { world?: Record<string, boolean>; unknown?: Record<string, string> } = $props();

  const names = $derived([...new Set([...Object.keys(world), ...Object.keys(unknown)])].sort());
</script>

{#if names.length}
  <table>
    <thead><tr><th>Condition</th><th>Value</th></tr></thead>
    <tbody>
      {#each names as name (name)}
        {@const err = unknown[name]}
        <tr>
          <td><code>{name}</code></td>
          <td>
            {#if err !== undefined}
              <span class="v unknown" title={err}>unknown</span>
              <span class="err">{err}</span>
            {:else if world[name]}
              <span class="v yes">true</span>
            {:else}
              <span class="v no">false</span>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
{:else}
  <p class="empty">World state not yet evaluated.</p>
{/if}

<style>
  .v {
    display: inline-block;
    min-width: 4.2rem;
    text-align: center;
    font-size: 0.78rem;
    font-weight: 700;
    border-radius: var(--radius-sm);
    padding: 0 0.4rem;
  }
  .yes {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .no {
    background: var(--neutral-soft);
    color: var(--muted);
  }
  .unknown {
    background: var(--warn-soft);
    color: var(--warn);
    cursor: help;
  }
  .err {
    margin-left: 0.5rem;
    color: var(--muted);
    font-size: 0.8rem;
  }
</style>
