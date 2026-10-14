<script lang="ts">
  // Rules a decision point of the change: the point (preselected when only one is pending), its option and why.
  import type { WidgetProps } from '../registry';
  import type { DecisionValue } from '../types';

  let { id, field, value, onchange, source, disabled, invalid }: WidgetProps = $props();
  const pending = $derived((source.decisions ?? []).filter((d) => !d.decided));
  const v = $derived<DecisionValue>(value && typeof value === 'object' ? (value as DecisionValue) : {});
  const point = $derived(v.point || (pending.length === 1 ? pending[0].id : ''));
  const current = $derived(pending.find((d) => d.id === point));

  function put(patch: DecisionValue) {
    onchange({ ...v, point, ...patch });
  }
</script>

<div class="decision" {id}>
  {#if !source.decisions}
    <p class="muted">The decision points of the change are not known here.</p>
  {:else if !pending.length}
    <p class="muted">No decision point of the change is waiting for a ruling.</p>
  {:else}
    {#if pending.length > 1}
      <select aria-label="Decision point" value={point} {disabled} aria-invalid={invalid} onchange={(e) => onchange({ point: e.currentTarget.value || undefined })}>
        <option value="">decision point…</option>
        {#each pending as d (d.id)}<option value={d.id}>{d.question}</option>{/each}
      </select>
    {:else}
      <p class="q">{current?.question}</p>
    {/if}
    {#if current}
      {#if current.options.length}
        <div class="options" role="radiogroup" aria-label="Options">
          {#each current.options as o (o.id)}
            <label class="opt"><input type="radio" name="{id}-opt" value={o.id} checked={v.option === o.id} {disabled} onchange={() => put({ option: o.id })} /> {o.label}</label>
          {/each}
        </div>
      {:else}
        <input type="text" aria-label="Answer" placeholder="the answer" value={v.option ?? ''} {disabled} oninput={(e) => put({ option: e.currentTarget.value || undefined })} />
      {/if}
      <textarea rows="2" aria-label="Justification" placeholder={field.constraints?.['justify'] ? 'Why (required)' : 'Why'} value={v.justification ?? ''} {disabled}
        oninput={(e) => put({ justification: e.currentTarget.value || undefined })}></textarea>
    {/if}
  {/if}
</div>

<style>
  .decision {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .q {
    margin: 0;
    font-weight: 500;
  }
  .options {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .opt {
    font-weight: 400;
    display: flex;
    gap: 0.35rem;
    align-items: center;
  }
  .muted {
    color: var(--muted);
    margin: 0;
    font-size: 0.85rem;
  }
</style>
