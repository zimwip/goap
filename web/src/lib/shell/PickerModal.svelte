<script lang="ts">
  // Single global instance (mounted once in Shell.svelte, like FieldDialog): a searchable list in a modal,
  // filtered client-side over the items the caller passed (already-loaded graph nodes, so no round trip).
  import { picker, closePicker } from './pickerState.svelte';

  let text = $state('');
  let cursor = $state(0);
  let input = $state<HTMLInputElement>();

  $effect(() => {
    if (!picker.open) return;
    text = '';
    cursor = 0;
    queueMicrotask(() => input?.focus());
  });

  const filtered = $derived.by(() => {
    const q = text.trim().toLowerCase();
    const items = picker.items;
    if (!q) return items;
    return items.filter((i) => i.label.toLowerCase().includes(q) || i.key.toLowerCase().includes(q) || i.hint?.toLowerCase().includes(q));
  });

  function choose(key: string) {
    picker.onchoose?.(key);
    closePicker();
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      closePicker();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      cursor = Math.min(cursor + 1, filtered.length - 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      cursor = Math.max(cursor - 1, 0);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const item = filtered[cursor];
      if (item) choose(item.key);
    }
  }
</script>

{#if picker.open}
  <div class="backdrop" role="presentation" onmousedown={closePicker}>
    <div class="dialog" role="dialog" aria-label={picker.title} tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
      <h3>{picker.title}</h3>
      <input bind:this={input} bind:value={text} oninput={() => (cursor = 0)} onkeydown={onKeydown} type="text" placeholder={picker.placeholder} />
      <ul class="list" role="listbox">
        {#each filtered as item, i (item.key)}
          <li>
            <button type="button" class="opt" class:active={i === cursor} onmouseenter={() => (cursor = i)} onclick={() => choose(item.key)}>
              <span class="label">{item.label}</span>
              {#if item.hint}<span class="hint-text">{item.hint}</span>{/if}
            </button>
          </li>
        {:else}
          <li class="empty">No match.</li>
        {/each}
      </ul>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 96;
    display: grid;
    place-items: center;
    background: rgba(0, 0, 0, 0.4);
  }
  .dialog {
    width: min(420px, calc(100vw - 28px));
    max-height: min(480px, calc(100vh - 40px));
    display: grid;
    grid-template-rows: auto auto 1fr;
    gap: 0.5rem;
    padding: 1rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  h3 {
    margin: 0;
  }
  input {
    width: 100%;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    overflow: auto;
    display: grid;
    gap: 0.1rem;
  }
  .opt {
    width: 100%;
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
    text-align: left;
    padding: 0.35rem 0.5rem;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text);
    cursor: pointer;
  }
  .opt.active {
    background: var(--accent-soft, var(--hover));
  }
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .hint-text {
    color: var(--muted);
    font-size: 0.85em;
    white-space: nowrap;
  }
  .empty {
    color: var(--muted);
    padding: 0.4rem 0.5rem;
  }
</style>
