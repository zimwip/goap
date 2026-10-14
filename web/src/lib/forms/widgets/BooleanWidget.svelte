<script lang="ts">
  import type { WidgetProps } from '../registry';

  let { id, field, value, onchange, disabled }: WidgetProps = $props();
</script>

<label class="check">
  <input {id} type="checkbox" checked={value === true} {disabled} onchange={(e) => onchange(e.currentTarget.checked)} />
  {field.placeholder ?? 'Yes'}
</label>

<style>
  .check {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0;
    font-weight: 400;
  }
</style>
