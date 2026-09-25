<script lang="ts">
  import { shortId, type BoardIssue } from '../api';

  let { issues, onitem }: { issues: BoardIssue[]; onitem?: (id: string) => void } = $props();
</script>

<ul class="issues">
  {#each issues as i, k (k)}
    <li class:warning={i.severity === 'warning'}>
      <span class="sev {i.severity}">{i.severity}</span>
      <span class="chip">{i.code}</span>
      <span class="msg">{i.message}</span>
      {#if i.item}
        <span class="ref">
          item
          {#if onitem}<button type="button" class="link mono" onclick={() => onitem?.(i.item ?? '')}>{shortId(i.item)}</button>
          {:else}<code>{shortId(i.item)}</code>{/if}
          {#if i.culprit && i.culprit !== i.item}
            · blame
            {#if onitem}<button type="button" class="link mono" onclick={() => onitem?.(i.culprit ?? '')}>{shortId(i.culprit)}</button>
            {:else}<code>{shortId(i.culprit)}</code>{/if}
          {/if}
        </span>
      {/if}
    </li>
  {/each}
</ul>

<style>
  .issues {
    list-style: none;
    padding: 0;
    margin: 8px 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px;
  }
  .sev {
    font-size: 0.75em;
    text-transform: uppercase;
    color: var(--danger);
  }
  li.warning .sev {
    color: var(--warn);
  }
  .chip {
    font-size: 0.75em;
    padding: 0 6px;
    border: 1px solid var(--border, currentColor);
    border-radius: 8px;
  }
  .ref {
    color: var(--muted);
    font-size: 0.85em;
  }
</style>
