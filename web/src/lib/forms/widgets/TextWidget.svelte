<script lang="ts">
  import type { WidgetProps } from '../registry';

  let { id, field, value, onchange, disabled, invalid }: WidgetProps = $props();
  const text = $derived(typeof value === 'string' ? value : '');
  const c = $derived(field.constraints ?? {});
  const maxLength = $derived(typeof c['maxLength'] === 'number' ? (c['maxLength'] as number) : undefined);
</script>

{#if field.widget === 'textarea'}
  <textarea {id} rows="3" value={text} placeholder={field.placeholder} maxlength={maxLength} {disabled} aria-invalid={invalid}
    oninput={(e) => onchange(e.currentTarget.value)}></textarea>
{:else if field.widget === 'date'}
  <input {id} type="date" value={text} min={c['min'] as string | undefined} max={c['max'] as string | undefined} {disabled} aria-invalid={invalid}
    oninput={(e) => onchange(e.currentTarget.value)} />
{:else}
  <input {id} type="text" value={text} placeholder={field.placeholder} maxlength={maxLength} {disabled} aria-invalid={invalid}
    oninput={(e) => onchange(e.currentTarget.value)} />
{/if}
