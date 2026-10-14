<script lang="ts">
  // The fallback of a widget the web does not know: its value as JSON, its constraints listed.
  import { untrack } from 'svelte';
  import type { WidgetProps } from '../registry';

  let { id, field, value, onchange, disabled, invalid }: WidgetProps = $props();
  let text = $state(untrack(() => (value === undefined ? '' : JSON.stringify(value, null, 2))));
  let error = $state('');
  function input(s: string) {
    text = s;
    if (!s.trim()) {
      error = '';
      onchange(undefined);
      return;
    }
    try {
      onchange(JSON.parse(s));
      error = '';
    } catch {
      error = 'Invalid JSON';
    }
  }
</script>

<textarea {id} class="mono" rows="3" value={text} {disabled} aria-invalid={invalid || !!error} oninput={(e) => input(e.currentTarget.value)}></textarea>
<p class="hint">
  Widget <code>{field.widget}</code> is not available here: enter its value as JSON.
  {#if field.constraints && Object.keys(field.constraints).length}Constraints: <code>{JSON.stringify(field.constraints)}</code>{/if}
  {#if error}<span class="err">{error}</span>{/if}
</p>

<style>
  .hint {
    color: var(--muted);
    font-size: 0.8rem;
    margin: 0.2rem 0 0;
  }
  .err {
    color: var(--danger);
  }
</style>
