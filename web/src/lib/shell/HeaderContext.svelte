<script lang="ts">
  // The context, in the header: where the person works (project, working change). The sections of the editor shown
  // are in the context bar on the right.
  import ProjectSelector from './ProjectSelector.svelte';
  import WorkingChangeSelector from './WorkingChangeSelector.svelte';
</script>

<div class="hctx" aria-label="Context" role="group">
  <ProjectSelector />
  <WorkingChangeSelector />
</div>

<style>
  .hctx {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1 1 0;
    min-width: 0;
  }
  /* a narrow header: the project and the working change by their icons (their name in the tooltip) */
  @media (max-width: 1100px) {
    .hctx :global(.pl),
    .hctx :global(.wl) {
      display: none;
    }
  }
</style>
