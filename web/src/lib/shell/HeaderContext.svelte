<script lang="ts">
  // The context, in the header: where the person works (project, working change, the option of it worked on). The sections of the editor shown
  // are the tabs of the editor.
  import ProjectSelector from './ProjectSelector.svelte';
  import WorkingChangeSelector from './WorkingChangeSelector.svelte';
  import OptionSelector from './OptionSelector.svelte';
</script>

<div class="hctx" aria-label="Context" role="group">
  <ProjectSelector />
  <WorkingChangeSelector />
  <OptionSelector />
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
    .hctx :global(.wl),
    .hctx :global(.ol) {
      display: none;
    }
  }
</style>
