<script lang="ts">
  // Édition d'une map de conditions (pre / effects) sous forme de lignes :
  // condition + valeur attendue (vrai / faux).
  import type { CondRow } from '../methodologyForm';
  import RowTools from './RowTools.svelte';
  import { moveItem } from '../methodologyForm';

  let {
    rows = $bindable(),
    options,
    path,
    label,
    bad,
    readonly = false,
  }: {
    rows: CondRow[];
    options: string[];
    /** chemin des problèmes, ex. « actions[0].pre » */
    path: string;
    label: string;
    bad: (path: string, exact?: boolean) => boolean;
    readonly?: boolean;
  } = $props();

  const used = $derived(new Set(rows.map((r) => r.cond)));

  function add() {
    const free = options.find((o) => !used.has(o)) ?? '';
    rows.push({ cond: free, value: true });
  }
</script>

<div class="conds" class:bad={bad(path, true)} data-path={path}>
  <div class="label">{label}</div>
  {#each rows as row, i}
    <div class="crow" class:bad={!!row.cond && bad(`${path}.${row.cond}`)}>
      <select bind:value={row.cond} aria-label={`${label} : condition`} data-path={`${path}.${row.cond}`}>
        {#if !row.cond}<option value="">— condition —</option>{/if}
        {#if row.cond && !options.includes(row.cond)}<option value={row.cond}>{row.cond} (inconnue)</option>{/if}
        {#each options as o (o)}
          <option value={o} disabled={o !== row.cond && used.has(o)}>{o}</option>
        {/each}
      </select>
      <button
        type="button"
        class="small toggle"
        class:on={row.value}
        aria-pressed={row.value}
        title="Valeur attendue (cliquer pour inverser)"
        onclick={() => (row.value = !row.value)}>{row.value ? 'vrai' : 'faux'}</button
      >
      {#if !readonly}
        <RowTools
          index={i}
          count={rows.length}
          label="la condition"
          onmove={(d) => moveItem(rows, i, d)}
          onremove={() => rows.splice(i, 1)}
        />
      {/if}
    </div>
  {:else}
    <div class="empty">Aucune condition.</div>
  {/each}
  {#if !readonly}
    <button type="button" class="small add" onclick={add}>+ condition</button>
  {/if}
</div>

<style>
  .conds {
    display: grid;
    gap: 0.35rem;
    padding: 0.5rem 0.6rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius-sm);
    min-width: 0;
  }
  .label {
    font-size: 0.85rem;
    font-weight: 600;
    color: var(--muted);
  }
  .crow {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    min-width: 0;
  }
  .crow select {
    flex: 1;
    min-width: 0;
    font-family: var(--mono);
    font-size: 0.85rem;
  }
  .toggle {
    flex: none;
    min-width: 3.4rem;
    background: var(--danger-soft);
    color: var(--danger);
    border-color: transparent;
  }
  .toggle.on {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .toggle:hover:not(:disabled) {
    filter: brightness(0.97);
  }
  .toggle.on:hover:not(:disabled) {
    background: var(--ok-soft);
  }
  .toggle:not(.on):hover:not(:disabled) {
    background: var(--danger-soft);
  }
  .add {
    justify-self: start;
  }
  .empty {
    font-size: 0.85rem;
  }
  .bad {
    border-color: var(--danger);
  }
  .crow.bad select {
    border-color: var(--danger);
    background: var(--danger-soft);
  }
</style>
