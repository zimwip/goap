<script lang="ts">
  // The Graph explorer tab (ADR 0036 §4): the explorer of the whole graph, components/GraphExplorer.svelte.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import GraphExplorer from '../../components/GraphExplorer.svelte';

  let { tab }: { tab: Tab } = $props();
  void untrack(() => tab);
</script>

<div class="editor-page explorer-page">
  <GraphExplorer />
</div>

<style>
  .explorer-page {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
</style>
