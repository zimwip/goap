<script lang="ts">
  import type { Draft } from '../../stores/drafts.svelte';
  let { draft, what }: { draft: Draft; what: string } = $props();
</script>

{#if draft.loading}
  <p class="empty">Chargement…</p>
{:else if draft.loadError}
  <div class="alert">{draft.loadError}</div>
{:else}
  <div class="alert warn">{what} introuvable dans le brouillon {draft.label} (supprimé ou renommé ?).</div>
{/if}
