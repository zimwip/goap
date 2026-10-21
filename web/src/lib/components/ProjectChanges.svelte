<script lang="ts">
  // The open changes of a change's project, one row each, the change shown marked: a change is read in the list of
  // the work around it. Folded by the person (remembered), at most a few rows then a scroll.
  import Icon from '../shell/Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import { changes, refreshChanges } from '../stores/catalog.svelte';
  import { rootProject } from '../stores/session.svelte';
  import { working, workable } from '../stores/workingChange.svelte';
  import { layout } from '../shell/layout.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { formatDate, shortId } from '../api';
  import type { Change } from '../api';

  let { change }: { change: Change } = $props();

  const FOLD = 'projectChanges';
  const folded = $derived(layout.navFolded.includes(FOLD));
  const projectOf = (c: Change) => c.projectId || rootProject();

  $effect(() => {
    if (!changes.loaded && !changes.loading) void refreshChanges();
  });

  const rows = $derived.by(() => {
    const p = projectOf(change);
    const open = changes.items.filter((c) => workable(c) && !c.parentId && projectOf(c) === p);
    const list = open.some((c) => c.id === change.id) ? open : [change, ...open];
    return [...list].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
  });
  const openCount = $derived(rows.filter((c) => workable(c)).length);

  function fold() {
    layout.navFolded = folded ? layout.navFolded.filter((x) => x !== FOLD) : [...layout.navFolded, FOLD];
  }

  function open(c: Change, pin: boolean) {
    if (c.id !== change.id) openTab({ kind: 'change', params: { id: c.id ?? '' } }, { pin });
  }
</script>

<section class="ui-card list" aria-label="Changes of the project">
  <button type="button" class="ui-card-head head" aria-expanded={!folded} onclick={fold}>
    <span class="chev" class:folded><Icon name="chevronDown" size={12} /></span>
    <Icon name="diff" size={14} />
    <span>Changes</span>
    <span class="ui-tag">{openCount} open</span>
    <span class="grow"></span>
    <span class="proj mono">{projectOf(change)}</span>
  </button>
  {#if !folded}
    <ul class="rows">
      {#each rows as c (c.id)}
        {@const on = c.id === change.id}
        <li>
          <button
            type="button"
            class="row"
            class:on
            aria-current={on ? 'page' : undefined}
            title={on ? 'The change shown' : 'Open this change (double click: keep its tab)'}
            onclick={() => open(c, false)}
            ondblclick={() => open(c, true)}
            onauxclick={(e) => e.button === 1 && open(c, true)}
          >
            <span class="who">{c.ownerOrg || '—'}</span>
            <span class="mono when">{formatDate(c.createdAt)}</span>
            <span class="mono what"><span class="id">#{shortId(c.id)}</span> · {c.title || 'Untitled'}</span>
            {#if working.change?.id === c.id}<span class="ui-tag work" title="The change you work in">working</span>{/if}
            <StatusBadge status={c.status} />
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .list {
    margin-bottom: var(--space-3);
  }
  .head {
    width: 100%;
    border: none;
    border-radius: 0;
    background: none;
    text-align: left;
    cursor: pointer;
    font-size: 11.5px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .head:hover:not(:disabled) {
    background: var(--bg-raised);
  }
  .chev {
    display: inline-flex;
    color: var(--text-3);
    transition: transform var(--transition);
  }
  .chev.folded {
    transform: rotate(-90deg);
  }
  .list:has(.chev.folded) .head {
    border-bottom: none;
  }
  .ui-tag {
    text-transform: none;
    letter-spacing: 0;
  }
  .proj {
    color: var(--text-3);
    text-transform: none;
    letter-spacing: 0;
    font-weight: 400;
  }
  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
    /* five rows, then a scroll */
    max-height: calc(5 * 35px);
    overflow: auto;
  }
  .rows li + li {
    border-top: 1px solid var(--border);
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    width: 100%;
    min-height: 34px;
    padding: 0 var(--space-4);
    border: none;
    border-left: 2px solid transparent;
    border-radius: 0;
    background: none;
    text-align: left;
    font-weight: 400;
    color: var(--text-2);
  }
  .row:hover:not(:disabled) {
    background: var(--bg-raised);
    color: var(--text);
  }
  .row.on {
    background: var(--accent-subtle);
    border-left-color: var(--accent);
    color: var(--text);
  }
  .who {
    font-weight: 600;
    color: var(--text);
    white-space: nowrap;
  }
  .when {
    color: var(--text-3);
    white-space: nowrap;
    font-size: 11px;
  }
  .what {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
  }
  .id {
    color: var(--text-3);
  }
  .work {
    color: var(--accent);
    border-color: var(--accent);
  }
  @media (max-width: 1024px) {
    .when,
    .who {
      display: none;
    }
  }
</style>
