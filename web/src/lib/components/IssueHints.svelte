<script lang="ts">
  // The issues of a field of a methodology draft, under it, each with how to fix it (Issue.hint): the composition
  // checks of the registry (ADR 0106) give one.
  import Icon from '../shell/Icon.svelte';
  import { isWarning } from '../issues';
  import type { Draft } from '../stores/drafts.svelte';

  let { draft, path }: { draft: Draft; path: string } = $props();

  const issues = $derived(draft.readonly ? [] : draft.issuesAt(path));
</script>

{#if issues.length}
  <ul class="issue-hints">
    {#each issues as i, k (k)}
      <li class:warning={isWarning(i)}>
        <Icon name={isWarning(i) ? 'bell' : 'alert'} size={12} />
        <span>
          {i.message}
          {#if i.hint}<span class="fix">Fix: {i.hint}</span>{/if}
        </span>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .issue-hints {
    list-style: none;
    margin: 2px 0 6px;
    padding: 0;
    font-size: 0.85em;
  }
  li {
    display: flex;
    gap: 0.4rem;
    align-items: baseline;
    color: var(--danger);
  }
  li.warning {
    color: var(--warn);
  }
  .fix {
    display: block;
    color: var(--muted);
  }
</style>
