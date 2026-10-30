<script lang="ts">
  import type { WidgetProps } from '../registry';
  import { strings } from '../validate';

  let { id, field, value, onchange, disabled, invalid }: WidgetProps = $props();
  const options = $derived(strings(field.constraints?.['options']));
  const labels = $derived(strings(field.constraints?.['labels']));
</script>

<select {id} value={typeof value === 'string' ? value : ''} {disabled} aria-invalid={invalid} onchange={(e) => onchange(e.currentTarget.value || undefined)}>
  <option value="">{field.placeholder ?? '—'}</option>
  {#each options as o, i (o)}<option value={o}>{labels[i] || o}</option>{/each}
</select>
