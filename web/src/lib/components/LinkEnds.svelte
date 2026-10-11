<script lang="ts">
  // Read-only view of a link type: its source node type, the link and its target node type side by side, each end with
  // its type hierarchy (supertypes and subtypes) and the other link types around it.
  import type { DomainDraft } from '../stores/domains.svelte';
  import { openLinkType, openNodeType } from '../views/editors/domainTabs';
  import { linksOf, subtypes, typeChain } from '../views/editors/typeLinks';

  let { d, index }: { d: DomainDraft; index: number } = $props();

  const f = $derived(d.form);
  const link = $derived(f.linkTypes[index]);

  const ends = $derived(
    link
      ? [
          { side: 'Source', type: link.from.trim(), end: 'from' as const },
          { side: 'Target', type: link.to.trim(), end: 'to' as const },
        ].map((e) => {
          const at = f.nodeTypes.findIndex((t) => t.name.trim() === e.type);
          const parents = [...typeChain(f.nodeTypes, e.type).entries()].filter(([, n]) => n > 0).map(([name]) => name);
          return {
            ...e,
            at,
            node: at >= 0 ? f.nodeTypes[at] : undefined,
            parents,
            subs: e.type ? subtypes(f.nodeTypes, e.type) : [],
            outgoing: e.type ? linksOf(f.nodeTypes, f.linkTypes, e.type, 'from') : [],
            ingoing: e.type ? linksOf(f.nodeTypes, f.linkTypes, e.type, 'to') : [],
          };
        })
      : [],
  );
</script>

{#snippet rows(list: ReturnType<typeof linksOf>, dir: 'out' | 'in')}
  {#if list.length}
    <ul class="plain-list">
      {#each list as r (r.index)}
        <li class:current={r.index === index}>
          {#if dir === 'out'}<code>{r.name}</code> → <code>{r.other || '(any)'}</code>{:else}<code>{r.other || '(any)'}</code> <span class="hint">via</span> <code>{r.name}</code>{/if}
          {#if r.index === index}<span class="hint">· this link type</span>
          {:else}<button type="button" class="small link" title="Open the link type {r.name}" onclick={() => openLinkType(d, r.index)}>Open ↗</button>{/if}
          {#if r.inheritedFrom}<span class="hint">· inherited from {r.inheritedFrom}</span>{/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p class="hint">none</p>
  {/if}
{/snippet}

{#if link}
  <div class="ends">
    {#each ends as e, k (e.end)}
      {#if k === 1}
        <div class="mid" aria-label="the link type">
          <code>{link.name || '(unnamed)'}</code>
          <span class="arrow">→</span>
          {#if link.compose}<span class="hint">composition: the target is a part of the source</span>{/if}
        </div>
      {/if}
      <div class="end">
        <h4>{e.side} <span class="hint">node type</span></h4>
        {#if e.node}
          <p class="name">
            <code>{e.type}</code>
            <button type="button" class="small link" title="Open the node type {e.type}" onclick={() => openNodeType(d, e.at)}>Open ↗</button>
          </p>
          {#if e.node.description}<p class="hint">{e.node.description}</p>{/if}
        {:else if e.type}
          <p class="name"><code>{e.type}</code> <span class="hint">(unknown node type)</span></p>
        {:else}
          <p class="hint">not set</p>
        {/if}
        <h5>Hierarchy</h5>
        {#if e.parents.length || e.subs.length}
          <ul class="plain-list">
            {#each [...e.parents].reverse() as p, i (p)}<li style:padding-left="{i * 0.8}rem"><code>{p}</code> <span class="hint">parent</span></li>{/each}
            <li class="current" style:padding-left="{e.parents.length * 0.8}rem"><code>{e.type}</code></li>
            {#each e.subs as s (s.name)}<li style:padding-left="{(e.parents.length + s.depth) * 0.8}rem"><code>{s.name}</code> <span class="hint">subtype</span></li>{/each}
          </ul>
        {:else}
          <p class="hint">no parent, no subtype</p>
        {/if}
        <h5>Outgoing links</h5>
        {@render rows(e.outgoing, 'out')}
        <h5>Ingoing links</h5>
        {@render rows(e.ingoing, 'in')}
      </div>
    {/each}
  </div>
{/if}

<style>
  .ends {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
    gap: 0.75rem;
    align-items: start;
  }
  .end {
    min-width: 0;
  }
  .mid {
    display: grid;
    justify-items: center;
    gap: 0.25rem;
    padding-top: 1.6rem;
    max-width: 12rem;
    text-align: center;
  }
  .arrow {
    font-size: 1.4rem;
  }
  h4,
  h5 {
    margin: 0 0 0.3rem;
  }
  h5 {
    margin-top: 0.7rem;
    font-size: 0.8rem;
    text-transform: uppercase;
    opacity: 0.7;
  }
  .name {
    margin: 0 0 0.25rem;
  }
  .plain-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.2rem;
  }
  li.current {
    font-weight: 600;
  }
  @media (max-width: 800px) {
    .ends {
      grid-template-columns: 1fr;
    }
  }
</style>
