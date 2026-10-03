<script lang="ts">
  // Who else is looking at a tab, as small round initials (the people of the platform, live: ADR 0053).
  import { viewersOf } from '../flux/presence.svelte';

  let { tabId, max = 3 }: { tabId: string; max?: number } = $props();

  const viewers = $derived(viewersOf(tabId));
  const shown = $derived(viewers.slice(0, max));
  const more = $derived(viewers.length - shown.length);
  const names = $derived(viewers.map((v) => (v.me ? `${v.subject} (you, on another window)` : v.subject)).join(', '));
</script>

{#if viewers.length}
  <span class="presence" title={`Looking at this: ${names}`} aria-label={`Also looking at this: ${names}`}>
    {#each shown as v (v.subject)}
      <span class="av" class:me={v.me} style:background={v.color}>{v.initials}</span>
    {/each}
    {#if more > 0}<span class="av more">+{more}</span>{/if}
  </span>
{/if}

<style>
  .presence {
    display: inline-flex;
    align-items: center;
    flex: none;
  }
  .av {
    display: inline-grid;
    place-items: center;
    width: 18px;
    height: 18px;
    border-radius: 50%;
    border: 1.5px solid var(--bg, #fff);
    margin-left: -5px;
    font-size: 0.58rem;
    font-weight: 600;
    line-height: 1;
    color: #fff;
  }
  .presence > .av:first-child {
    margin-left: 0;
  }
  .av.me {
    opacity: 0.6;
  }
  .av.more {
    background: var(--muted, #6b7280);
  }
</style>
