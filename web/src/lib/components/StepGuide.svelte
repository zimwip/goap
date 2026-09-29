<script lang="ts">
  // The step of a process a task belongs to (ADR 0034, ADR 0035 §2): where it stands in the process, what it is for,
  // how to go about it, what to check, what it produces and where it is described.
  import type { StepContext } from '../api';
  import { renderMarkdown } from '../markdown';

  let { context }: { context: StepContext } = $props();

  // the checklist is ticked by the person doing the task, in this view only (a reminder, not a record)
  let ticked = $state<Record<number, boolean>>({});
  const isUrl = (r: string) => /^https?:\/\//.test(r);
</script>

<div class="guide">
  <div class="where">
    Step <strong>{context.name}</strong> of the process <strong>{context.process}</strong>
    <span class="hint mono">{context.path}</span>
  </div>
  {#if context.description}<p class="desc">{context.description}</p>{/if}
  {#if context.guidance}<div class="md">{@html renderMarkdown(context.guidance)}</div>{/if}
  {#if context.checklist?.length}
    <ul class="checklist" aria-label="Checklist">
      {#each context.checklist as item, i (i)}
        <li><label><input type="checkbox" bind:checked={ticked[i]} /> {item}</label></li>
      {/each}
    </ul>
  {/if}
  {#if context.deliverables?.length}
    <p class="hint">Produces: {context.deliverables.join(', ')}</p>
  {/if}
  {#if context.references?.length}
    <ul class="refs" aria-label="Reference documents">
      {#each context.references as r, i (i)}
        <li>
          {#if isUrl(r.ref ?? '')}
            <a href={r.ref} target="_blank" rel="noreferrer">{r.title || r.ref}</a>
          {:else}
            {r.title || r.ref} <code>{r.ref}</code>
          {/if}
          {#if r.section}<span class="hint"> — {r.section}</span>{/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .guide {
    border-left: 3px solid var(--accent);
    padding: 4px 10px;
    margin: 6px 0 10px;
    background: var(--accent-soft);
    border-radius: var(--radius-sm);
  }
  .where {
    margin-bottom: 4px;
  }
  .checklist,
  .refs {
    margin: 4px 0;
    padding-left: 1.2em;
  }
  .checklist {
    list-style: none;
    padding-left: 0;
  }
</style>
