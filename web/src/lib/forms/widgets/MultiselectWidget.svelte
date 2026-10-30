<script lang="ts">
  import type { WidgetProps } from '../registry';
  import { strings } from '../validate';

  let { id, field, value, onchange, disabled }: WidgetProps = $props();
  const options = $derived(strings(field.constraints?.['options']));
  /** what each option shows (the labels constraint), else its value */
  const labels = $derived(strings(field.constraints?.['labels']));
  const chosen = $derived(strings(value));
  function toggle(o: string, on: boolean) {
    onchange(on ? [...chosen, o] : chosen.filter((x) => x !== o));
  }
</script>

<div class="options" {id} role="group">
  {#each options as o, i (o)}
    <label><input type="checkbox" checked={chosen.includes(o)} {disabled} onchange={(e) => toggle(o, e.currentTarget.checked)} /> {labels[i] || o}</label>
  {/each}
</div>

<style>
  .options {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 1rem;
  }
  label {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    margin: 0;
    font-weight: 400;
  }
</style>
