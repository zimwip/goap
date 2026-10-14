<script lang="ts">
  import type { WidgetProps } from '../registry';

  let { id, field, value, onchange, disabled, invalid }: WidgetProps = $props();
  const c = $derived(field.constraints ?? {});
  const n = (k: string) => (typeof c[k] === 'number' ? (c[k] as number) : undefined);
</script>

<input {id} type="number" value={typeof value === 'number' ? value : ''} min={n('min')} max={n('max')} step={n('step') ?? (c['integer'] ? 1 : 'any')}
  placeholder={field.placeholder} {disabled} aria-invalid={invalid}
  oninput={(e) => onchange(e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value))} />
