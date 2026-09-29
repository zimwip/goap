<script lang="ts">
  // Reference documents of a process or a step (ADR 0034): where it is described in the documentary repository —
  // a document of the graph ("doc:<key>"), a document repository reached through an MCP ("<mcp>:<path>") or a URL.
  import RowTools from '../../components/RowTools.svelte';
  import { moveItem, type ReferenceForm } from '../../methodologyForm';

  let {
    refs = $bindable(),
    path,
    bad,
    readonly = false,
  }: {
    refs: ReferenceForm[];
    /** issue path, e.g. "processes[0].references" */
    path: string;
    bad: (path: string, exact?: boolean) => boolean;
    readonly?: boolean;
  } = $props();
</script>

<div class="refs" data-path={path}>
  <div class="label">Reference documents</div>
  {#each refs as r, i}
    <div class="rrow" class:bad={bad(`${path}[${i}]`)}>
      <input type="text" aria-label="Title" placeholder="Title" bind:value={r.title} />
      <input
        type="text"
        class="mono"
        aria-label="Document"
        placeholder="document-repository:guide.md, doc:KEY or https://…"
        bind:value={r.ref}
        class:bad={bad(`${path}[${i}].ref`)}
        data-path="{path}[{i}].ref"
      />
      <input type="text" aria-label="Section" placeholder="Section" bind:value={r.section} />
      {#if !readonly}
        <RowTools index={i} count={refs.length} label="the reference" onmove={(d) => moveItem(refs, i, d)} onremove={() => refs.splice(i, 1)} />
      {/if}
    </div>
  {:else}
    <p class="hint">None.</p>
  {/each}
  {#if !readonly}
    <button type="button" class="small" onclick={() => refs.push({ title: '', ref: '', section: '' })}>+ Reference</button>
  {/if}
</div>

<style>
  .refs {
    margin: 6px 0;
  }
  .label {
    font-weight: 600;
    margin-bottom: 4px;
  }
  .rrow {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 2fr) minmax(0, 1fr) auto;
    gap: 6px;
    align-items: center;
    margin-bottom: 4px;
  }
  @media (max-width: 700px) {
    .rrow {
      grid-template-columns: 1fr;
    }
  }
</style>
