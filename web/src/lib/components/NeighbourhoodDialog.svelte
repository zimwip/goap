<script lang="ts">
  // The neighbourhood of a node (or of a link: both of its ends) of a commit, in a modal: the graph of
  // NodeNeighbourhood, opened on demand from the detail panel of the Commit tab. A click recenters, a double-click
  // opens the node in its editor; the node or link shown stays bound to the host, so its detail panel follows.
  import type { Link } from '../api';
  import Icon from '../shell/Icon.svelte';
  import NodeNeighbourhood from './NodeNeighbourhood.svelte';

  let {
    commitId,
    namespace,
    branch,
    node = $bindable(''),
    link = $bindable(),
    reload = 0,
    onclose,
  }: {
    commitId: string;
    namespace: string;
    branch?: string;
    node?: string;
    link?: Link;
    reload?: number;
    onclose: () => void;
  } = $props();
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose()} />

<div class="backdrop" role="presentation" onmousedown={() => onclose()}>
  <div class="panel" role="dialog" aria-modal="true" aria-label="Neighbourhood" tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
    <header class="head">
      <h2>Neighbourhood</h2>
      <span class="grow"></span>
      <button type="button" class="ghost small" aria-label="Close" onclick={() => onclose()}><Icon name="x" size={14} /></button>
    </header>
    <div class="body">
      <NodeNeighbourhood {commitId} {branch} {namespace} bind:node bind:link {reload} details={false} empty="Select a node to see its neighbours." />
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 97;
    display: grid;
    place-items: center;
    background: rgb(0 0 0 / 0.4);
  }
  .panel {
    width: min(1100px, calc(100vw - 32px));
    height: min(800px, calc(100vh - 32px));
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
    outline: none;
  }
  .head {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0.7rem 1rem;
    border-bottom: 1px solid var(--border);
    background: var(--chrome-2);
  }
  .head h2 {
    margin: 0;
    font-size: 1.05em;
  }
  .grow {
    flex: 1;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.8rem 1rem;
  }
</style>
