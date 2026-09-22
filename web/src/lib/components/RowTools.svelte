<script lang="ts">
  // Boutons de réordonnancement / suppression d'un élément de liste.
  let {
    index,
    count,
    label = 'élément',
    onmove,
    onremove,
  }: {
    index: number;
    count: number;
    label?: string;
    onmove: (delta: number) => void;
    onremove: () => void;
  } = $props();
</script>

<span class="tools">
  <button
    type="button"
    class="small icon"
    title="Monter"
    aria-label={`Monter ${label}`}
    disabled={index === 0}
    onclick={() => onmove(-1)}>↑</button
  >
  <button
    type="button"
    class="small icon"
    title="Descendre"
    aria-label={`Descendre ${label}`}
    disabled={index >= count - 1}
    onclick={() => onmove(1)}>↓</button
  >
  <button type="button" class="small icon danger" title="Supprimer" aria-label={`Supprimer ${label}`} onclick={onremove}
    >✕</button
  >
</span>

<style>
  .tools {
    display: inline-flex;
    gap: 0.25rem;
    flex: none;
  }
  .icon {
    min-width: 1.9rem;
    padding: 0.2rem 0.4rem;
  }
</style>
