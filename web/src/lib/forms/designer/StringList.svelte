<script lang="ts">
  // A list of strings as chips: type and Enter (or pick a suggestion) to add one, × to remove it.
  let {
    id,
    value = [],
    suggestions = [],
    placeholder = 'Add…',
    disabled = false,
    onchange,
  }: { id: string; value?: string[]; suggestions?: string[]; placeholder?: string; disabled?: boolean; onchange: (v: string[]) => void } = $props();

  let draft = $state('');
  const listId = $derived(`${id}-list`);
  const offered = $derived(suggestions.filter((s) => !value.includes(s)));

  function add(s: string) {
    const v = s.trim();
    if (v && !value.includes(v)) onchange([...value, v]);
    draft = '';
  }
</script>

<div class="list">
  {#each value as v (v)}
    <span class="chip"><code>{v}</code>{#if !disabled}<button type="button" aria-label="Remove {v}" onclick={() => onchange(value.filter((x) => x !== v))}>×</button>{/if}</span>
  {/each}
  {#if !disabled}
    <input
      {id}
      type="text"
      list={offered.length ? listId : undefined}
      bind:value={draft}
      {placeholder}
      onkeydown={(e) => {
        if (e.key === 'Enter' || e.key === ',') {
          e.preventDefault();
          add(draft);
        } else if (e.key === 'Backspace' && !draft && value.length) onchange(value.slice(0, -1));
      }}
      onchange={() => offered.includes(draft) && add(draft)}
      onblur={() => draft.trim() && add(draft)}
    />
    {#if offered.length}
      <datalist id={listId}>{#each offered as s (s)}<option value={s}></option>{/each}</datalist>
    {/if}
  {/if}
</div>

<style>
  .list {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
    align-items: center;
    padding: 0.2rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    padding: 0.05rem 0.35rem;
    border-radius: 999px;
    background: var(--surface-2);
    font-size: 0.8rem;
  }
  .chip button {
    border: none;
    background: none;
    padding: 0 0.1rem;
    line-height: 1;
    cursor: pointer;
    color: var(--muted);
  }
  input {
    flex: 1;
    min-width: 8rem;
    border: none;
    background: transparent;
    padding: 0.2rem;
  }
</style>
