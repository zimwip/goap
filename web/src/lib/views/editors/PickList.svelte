<script lang="ts">
  // Sélection multiple par cases à cocher ; liste vide = « tous ».
  let {
    selected = $bindable(),
    options,
    allLabel,
    label,
    path,
    bad,
    readonly = false,
    details = {},
  }: {
    selected: string[];
    options: string[];
    /** libellé de l'état « liste vide » */
    allLabel: string;
    label: string;
    path: string;
    bad: (path: string, exact?: boolean) => boolean;
    readonly?: boolean;
    details?: Record<string, string>;
  } = $props();

  let filter = $state('');
  const unknown = $derived(selected.filter((s) => !options.includes(s)));
  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived(options.filter((o) => !q || `${o} ${details[o] ?? ''}`.toLowerCase().includes(q)));

  function toggle(name: string, on: boolean) {
    if (on && !selected.includes(name)) selected = [...selected, name];
    else if (!on) selected = selected.filter((s) => s !== name);
  }
</script>

<div class="pick" class:bad={bad(path)} data-path={path}>
  <div class="head">
    <strong>{label}</strong>
    <span class="state" class:all={selected.length === 0}>
      {selected.length === 0 ? allLabel : `${selected.length} / ${options.length}`}
    </span>
    <span class="grow"></span>
    {#if !readonly}
      <button type="button" class="small ghost" disabled={selected.length === 0} onclick={() => (selected = [])}>Tous (vide)</button>
      <button type="button" class="small ghost" onclick={() => (selected = [...options])}>Tout cocher</button>
    {/if}
  </div>
  {#if options.length > 8}
    <input type="search" class="filter" placeholder="Filtrer…" aria-label={`Filtrer : ${label}`} bind:value={filter} data-no-pin />
  {/if}
  <div class="list" role="group" aria-label={label}>
    {#each shown as o (o)}
      {@const idx = selected.indexOf(o)}
      <label class="opt" class:bad={idx >= 0 && bad(`${path}[${idx}]`)}>
        <input type="checkbox" checked={idx >= 0} disabled={readonly} onchange={(e) => toggle(o, e.currentTarget.checked)} />
        <code>{o}</code>
        {#if details[o]}<span class="hint">{details[o]}</span>{/if}
      </label>
    {:else}
      <p class="empty">Aucun élément.</p>
    {/each}
    {#each unknown as u (u)}
      <label class="opt bad">
        <input type="checkbox" checked disabled={readonly} onchange={() => toggle(u, false)} />
        <code>{u}</code>
        <span class="hint">inconnu — décochez pour retirer</span>
      </label>
    {/each}
  </div>
</div>

<style>
  .pick {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.4rem 0.5rem;
    min-width: 0;
  }
  .pick.bad {
    border-color: var(--danger);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    flex-wrap: wrap;
    margin-bottom: 0.3rem;
  }
  .state {
    font-size: 0.85rem;
    color: var(--muted);
  }
  .state.all {
    color: var(--accent);
    font-weight: 600;
  }
  .filter {
    margin-bottom: 0.3rem;
  }
  .list {
    max-height: 16rem;
    overflow: auto;
    display: grid;
    gap: 1px;
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0;
    padding: 0.1rem 0.25rem;
    color: var(--text);
    font-weight: 400;
    border-radius: var(--radius-sm);
    cursor: pointer;
    min-width: 0;
  }
  .opt:hover {
    background: var(--hover);
  }
  .opt .hint {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .opt.bad code {
    color: var(--danger);
  }
</style>
