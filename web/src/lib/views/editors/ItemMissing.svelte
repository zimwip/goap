<script lang="ts">
  import type { Draft } from '../../stores/drafts.svelte';
  let { draft, what }: { draft: Draft; what: string } = $props();
</script>

{#if draft.loading}
  <p class="empty">Loading…</p>
{:else if draft.loadError}
  <div class="alert">{draft.loadError}</div>
{:else}
  <div class="alert warn">{what} not found in draft {draft.label} (deleted or renamed?).</div>
{/if}
