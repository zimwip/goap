<script lang="ts">
  // Common header for a draft's tabs: breadcrumb, status, errors.
  import Icon, { type IconName } from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { methodologySpec } from './methodologyTabs';
  import type { Draft } from '../../stores/drafts.svelte';

  let {
    draft,
    icon,
    kind,
    title,
    dirty,
  }: { draft: Draft; icon: IconName; kind: string; title: string; dirty: boolean } = $props();
</script>

<div class="crumbs">
  {#if draft.isNew}
    <span>New methodology</span>
  {:else}
    <button type="button" class="link" onclick={() => openTab(methodologySpec(draft.name, draft.version))}>{draft.label}</button>
  {/if}
  <span aria-hidden="true">›</span>
  <span>{kind}</span>
</div>
<div class="editor-head">
  <Icon name={icon} size={18} />
  <h2>{title}</h2>
  <StatusBadge status={draft.status} />
  {#if dirty}<span class="dirty" title="Unsaved changes">● modified</span>{/if}
</div>
{#if draft.error}<div class="alert">{draft.error}</div>{/if}
{#if draft.readonly && !draft.loading}
  <div class="alert info">
    {draft.status === 'published'
      ? 'Published version: it is immutable. Create a new version to modify it.'
      : 'Archived version: read-only.'}
  </div>
{/if}

<style>
  .crumbs {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    font-size: 0.88rem;
    color: var(--muted);
    margin-bottom: 0.25rem;
  }
  .dirty {
    color: var(--warn);
    font-size: 0.85rem;
    font-weight: 600;
  }
</style>
