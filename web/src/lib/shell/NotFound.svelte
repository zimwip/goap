<script lang="ts">
  // What an editor shows for an object that does not exist (any more): a stale link, another database, a deletion.
  import { closeTab } from './tabs.svelte';
  import { navigate } from './router';
  import type { Tab } from './types';

  let { tab, what }: { tab: Tab; what: string } = $props();

  function home() {
    void closeTab(tab.id, { force: true });
    navigate(undefined);
  }
</script>

<div class="notfound" role="alert">
  <h2>{what} not found</h2>
  <p class="hint">It was deleted, or the link points to another environment.</p>
  <button type="button" onclick={home}>Close and go home</button>
</div>

<style>
  .notfound {
    padding: 2rem;
    text-align: center;
  }
  .hint {
    color: var(--muted, gray);
  }
</style>
