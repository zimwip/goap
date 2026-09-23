<script lang="ts">
  // Buttons to reorder / remove a list item.
  let {
    index,
    count,
    label = 'item',
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
    title="Move up"
    aria-label={`Move ${label} up`}
    disabled={index === 0}
    onclick={() => onmove(-1)}>↑</button
  >
  <button
    type="button"
    class="small icon"
    title="Move down"
    aria-label={`Move ${label} down`}
    disabled={index >= count - 1}
    onclick={() => onmove(1)}>↓</button
  >
  <button type="button" class="small icon danger" title="Remove" aria-label={`Remove ${label}`} onclick={onremove}
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
