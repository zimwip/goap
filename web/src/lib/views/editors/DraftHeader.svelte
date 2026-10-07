<script lang="ts">
  // Common header for a draft's tabs: breadcrumb, status, errors.
  import Icon, { type IconName } from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { methodologySpec } from './methodologyTabs';
  import { splitIssues } from '../../issues';
  import type { Draft } from '../../stores/drafts.svelte';

  let {
    draft,
    icon,
    kind,
    title,
    dirty,
    path = '',
  }: { draft: Draft; icon: IconName; kind: string; title: string; dirty: boolean; path?: string } = $props();

  // the rules this element breaks (compilation and validation issues), with where each is
  const all = $derived(path ? draft.issuesAt(path) : []);
  const ruled = $derived(splitIssues(all).errors);
  const remarks = $derived(splitIssues(all).warnings);
  const where = (p: string | undefined) => (p ?? '').replace(path, '').replace(/^\./, '');
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
{#if ruled.length}
  <div class="alert broken" role="alert">
    <strong>{ruled.length} rule{ruled.length === 1 ? '' : 's'} broken</strong>
    <ul>
      {#each ruled as i, k (k)}
        <li>{#if where(i.norm)}<code>{where(i.norm)}</code> {/if}{i.message}</li>
      {/each}
    </ul>
  </div>
{/if}
{#if remarks.length}
  <div class="alert warn" role="status">
    <strong>{remarks.length} warning{remarks.length === 1 ? '' : 's'}</strong>
    <ul>
      {#each remarks as i, k (k)}
        <li>{i.message}</li>
      {/each}
    </ul>
  </div>
{/if}
{#if draft.remote}
  <div class="alert" role="alert">
    {draft.remote.actor || 'Someone'} {draft.remote.type.endsWith('deleted') ? 'removed' : draft.remote.type.endsWith('published') ? 'published' : 'saved'} this version
    while you were editing.
    <button type="button" class="small" onclick={() => draft.acceptRemote()}>Take theirs</button>
    <button type="button" class="ghost small" onclick={() => draft.keepMine()}>Keep mine</button>
  </div>
{/if}
{#if draft.error}<div class="alert">{draft.error}</div>{/if}
{#if draft.readonly && !draft.loading}
  <div class="alert info">
    {draft.status === 'published'
      ? 'Published version: it is immutable. Create a new version to modify it.'
      : 'Archived version: read-only.'}
  </div>
{/if}

<style>
  .broken ul {
    margin: 0.25rem 0 0;
    padding-left: 1.2rem;
  }
  .broken li {
    margin: 0.1rem 0;
  }
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
