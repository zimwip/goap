<script lang="ts">
  import type { WidgetProps } from '../registry';
  import { renderMarkdown } from '../../markdown';

  let { field }: WidgetProps = $props();
  const text = $derived(typeof field.constraints?.['text'] === 'string' ? (field.constraints['text'] as string) : '');
</script>

<div class="md">{@html renderMarkdown(text)}</div>

<style>
  .md :global(p) {
    margin: 0.25rem 0;
  }
</style>
