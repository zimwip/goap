<script lang="ts">
  // "Item type" tab (ADR 0119): a kind of item a change may hold, read-only (it changes with the platform code).
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { itemTypes, refreshItemTypes, itemTypeOf } from '../../stores/itemTypes.svelte';
  import { itemTypeShownIn, originLabel } from '../../itemTypes';

  let { tab }: { tab: Tab } = $props();

  $effect(() => {
    if (!itemTypes.loaded && !itemTypes.loading) void refreshItemTypes();
  });

  const kind = $derived(tab.params.kind ?? '');
  const x = $derived(itemTypeOf(kind));
</script>

<div class="editor-page">
  {#if !itemTypes.loaded}
    <p class="empty">Loading…</p>
  {:else if itemTypes.error}
    <div class="alert">{itemTypes.error}</div>
  {:else if !x}
    <div class="alert warn">The item type "{kind}" is not known to the platform.</div>
  {:else}
    <div class="editor-head">
      <Icon name="tag" size={18} />
      <h2 class="mono">{x.kind}</h2>
      <span class="hint">item type · {originLabel(x)}</span>
    </div>
    <div class="alert info">
      An item type is a kind of fact a change holds next to its impacts. Item types change with the platform code: the
      graph knows the built-in ones, and a use case registers its own.
    </div>
    <section class="card">
      <h3>General</h3>
      <dl class="props">
        <dt>Name</dt>
        <dd class="mono">{x.kind}</dd>
        <dt>Origin</dt>
        <dd>{originLabel(x)}</dd>
        <dt>Description</dt>
        <dd>{x.description || '—'}</dd>
        <dt>Shown in</dt>
        <dd>{itemTypeShownIn(x.kind ?? '')}</dd>
        <dt>Writing one asks</dt>
        <dd>
          {#if x.permission}
            the permission <code>{x.permission}</code>{#if x.subjectField}, and the writer must be the principal named by <code>data.{x.subjectField}</code>{/if}
          {:else}
            no permission of its own
          {/if}
        </dd>
      </dl>
    </section>
  {/if}
</div>

<style>
  .props {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.35rem 1rem;
    margin: 0;
  }
  .props dt {
    color: var(--muted);
  }
  .props dd {
    margin: 0;
  }
</style>
