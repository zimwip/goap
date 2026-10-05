<script lang="ts">
  // Criticality of a change (ADR 0075 §3), on its header: anyone who works on the change may raise it, lowering it asks a
  // permission the session tells (the server enforces it). What each level requires is policy of the organisation.
  import { graph, errorMessage, type Change } from '../api';
  import { criticalityOf, selectable, LEVEL_LABEL, LEVELS, type Level } from '../criticality';
  import { can } from '../stores/session.svelte';

  let { change, closed = false, onchange }: { change: Change; closed?: boolean; onchange?: () => void } = $props();

  const level = $derived(criticalityOf(change));
  const named = $derived(typeof (change.data as Record<string, unknown> | undefined)?.criticality === 'string');
  const options = $derived(selectable(level, can.lowerCriticality));
  let busy = $state(false);
  let error = $state('');

  async function set(to: Level) {
    if (!change.id || to === level) return;
    busy = true;
    error = '';
    try {
      await graph.updateChange(change.id, { data: { criticality: to } });
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<span class="crit">
  {#if closed || options.length < 2}
    <span class="chip {level}" title={named ? 'Criticality of the change' : 'Criticality of the change (the default)'}>{level}</span>
  {:else}
    <select class="chip {level}" value={level} disabled={busy} aria-label="Criticality of the change" onchange={(e) => set((e.currentTarget as HTMLSelectElement).value as Level)}>
      {#each LEVELS as l (l)}
        <option value={l} disabled={!options.includes(l)}>{LEVEL_LABEL[l]}</option>
      {/each}
    </select>
  {/if}
  {#if error}<span class="error">{error}</span>{/if}
</span>

<style>
  .crit {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .chip {
    font-weight: 600;
    border-radius: 999px;
    padding: 1px 8px;
    border: 1px solid var(--border);
    width: auto;
    background: var(--surface);
  }
  .C2 {
    border-color: var(--warn);
  }
  .C3 {
    border-color: var(--danger);
    color: var(--danger);
  }
</style>
