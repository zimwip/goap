<script lang="ts">
  // A search over the nodes a field may offer: type to filter by key, title or type, pick one. The choices are the
  // nodes the page holds; with `search`, the nodes the graph finds for the text too (NodeSource.search), kept when
  // `allow` takes them.
  import type { NodeOption } from '../types';
  import { typeName } from '../../stores/types.svelte';

  let {
    id,
    choices,
    exclude = [],
    placeholder = 'Search a node…',
    disabled = false,
    search,
    allow = () => true,
    onpick,
  }: {
    id?: string;
    choices: NodeOption[];
    exclude?: string[];
    placeholder?: string;
    disabled?: boolean;
    search?: (query: string) => Promise<NodeOption[]>;
    allow?: (n: NodeOption) => boolean;
    onpick: (n: NodeOption) => void;
  } = $props();

  const LIMIT = 30;
  const listId = `np-${Math.random().toString(36).slice(2, 8)}`;
  let q = $state('');
  let open = $state(false);
  let active = $state(0);
  /** the nodes the search found for the text typed */
  let found = $state<NodeOption[]>([]);
  $effect(() => {
    const s = q.trim();
    const find = search;
    if (!find || !open) return;
    let live = true;
    const t = setTimeout(() => {
      find(s)
        .then((r) => {
          if (live) found = r;
        })
        .catch(() => {
          if (live) found = [];
        });
    }, 200);
    return () => {
      live = false;
      clearTimeout(t);
    };
  });
  const hits = $derived.by(() => {
    const s = q.trim().toLowerCase();
    const seen = new Set<string>();
    return [...choices, ...found.filter(allow)]
      .filter((n) => !exclude.includes(n.key) && !seen.has(n.key) && (seen.add(n.key), true))
      .filter((n) => !s || `${n.key} ${n.title ?? ''} ${n.type}`.toLowerCase().includes(s))
      .slice(0, LIMIT);
  });

  function pick(n: NodeOption) {
    onpick(n);
    q = '';
    active = 0;
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      open = true;
      active = Math.min(active + 1, hits.length - 1);
      e.preventDefault();
    } else if (e.key === 'ArrowUp') {
      active = Math.max(active - 1, 0);
      e.preventDefault();
    } else if (e.key === 'Enter' && open && hits[active]) {
      pick(hits[active]);
      e.preventDefault();
    } else if (e.key === 'Escape') {
      open = false;
    }
  }
</script>

<div class="picker">
  <input
    {id}
    type="search"
    role="combobox"
    aria-expanded={open}
    aria-controls={listId}
    aria-autocomplete="list"
    autocomplete="off"
    bind:value={q}
    {placeholder}
    {disabled}
    onfocus={() => (open = true)}
    onblur={() => setTimeout(() => (open = false), 150)}
    oninput={() => ((open = true), (active = 0))}
    onkeydown={keydown}
  />
  {#if open && !disabled}
    <ul class="hits" id={listId} role="listbox">
      {#each hits as n, i (n.key)}
        <li role="option" aria-selected={i === active}>
          <button type="button" class:active={i === active} onmousedown={(e) => (e.preventDefault(), pick(n))}>
            <code>{n.key}</code>
            <span class="title">{n.title ?? ''}</span>
            <span class="type">{typeName(n.type)}{n.state ? ` · ${n.state}` : ''}</span>
          </button>
        </li>
      {:else}
        <li class="none">{choices.length || search ? 'No match.' : 'No node is allowed here.'}</li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .picker {
    position: relative;
  }
  .picker input {
    width: 100%;
  }
  .hits {
    position: absolute;
    z-index: 20;
    left: 0;
    right: 0;
    max-height: 16rem;
    overflow: auto;
    margin: 2px 0 0;
    padding: 0;
    list-style: none;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: 0 6px 18px rgb(0 0 0 / 0.12);
  }
  .hits button {
    display: grid;
    grid-template-columns: auto 1fr auto;
    gap: 0.5rem;
    width: 100%;
    text-align: left;
    border: none;
    border-radius: 0;
    background: none;
    padding: 0.3rem 0.6rem;
    font-weight: 400;
    color: var(--text);
  }
  .hits button.active,
  .hits button:hover {
    background: var(--surface-2);
  }
  .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .type,
  .none {
    color: var(--muted);
    font-size: 0.8rem;
  }
  .none {
    padding: 0.4rem 0.6rem;
  }
</style>
