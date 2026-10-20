<script lang="ts">
  // The folder chooser of a parameter whose widget is `directory` (ADR 0148): the directories the importer service reads,
  // listed by the service itself (a browser cannot name a path on the machine of a service). It opens on the directory
  // of the field when there is one, else on the roots; a git repository is flagged and picked by a click, any directory
  // by "Choose this directory".
  import { untrack } from 'svelte';
  import { errorMessage, imports, type BrowseListing } from '../api';
  import Icon from '../shell/Icon.svelte';

  let {
    domain,
    importer,
    param,
    start = '',
    onpick,
    onclose,
  }: {
    domain: string;
    importer: string;
    param: string;
    /** the directory to open on (the value of the field) */
    start?: string;
    onpick: (path: string) => void;
    onclose: () => void;
  } = $props();

  let path = $state(untrack(() => start));
  let listing = $state<BrowseListing | undefined>();
  let error = $state('');
  let loading = $state(false);

  $effect(() => {
    const at = path;
    const ctrl = new AbortController();
    untrack(() => {
      loading = true;
      error = '';
    });
    imports
      .browse({ domain, importer, param, path: at }, ctrl.signal)
      .then((l) => (listing = l))
      .catch((e) => {
        if (ctrl.signal.aborted) return;
        // a field that names no readable directory opens on the roots
        if (at && at === start && !listing) path = '';
        else error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) loading = false;
      });
    return () => ctrl.abort();
  });

  const entries = $derived(listing?.entries ?? []);
  const here = $derived(listing?.path ?? '');
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose()} />

<div class="backdrop" role="presentation" onmousedown={onclose}>
  <div class="panel" role="dialog" aria-modal="true" aria-label="Choose a directory" tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
    <header class="head">
      <Icon name="folder" size={16} />
      <h2>Choose a directory</h2>
      <span class="grow"></span>
      <button type="button" class="ghost small" aria-label="Close" onclick={onclose}><Icon name="x" size={14} /></button>
    </header>
    <div class="where">
      <button type="button" class="ghost small" disabled={!here} onclick={() => (path = listing?.parent ?? '')} title="Up">↑</button>
      <code class="path">{here || 'Directories the importer reads'}</code>
      {#if listing?.repository}<span class="badge">git repository</span>{/if}
    </div>
    <div class="body">
      {#if error}
        <div class="alert">{error}</div>
      {:else if !listing && loading}
        <p class="hint">Listing…</p>
      {:else if !entries.length}
        <p class="hint">{here ? 'No directory here.' : 'The importer reads no directory: set its roots (GOAP_IMPORT_GIT_ROOTS).'}</p>
      {:else}
        <ul class="entries" class:dim={loading}>
          {#each entries as e (e.path)}
            <li>
              <button type="button" class="entry" ondblclick={() => e.repository && onpick(e.path ?? '')} onclick={() => (path = e.path ?? '')}>
                <Icon name={e.repository ? 'branch' : 'folder'} size={14} />
                <span class="name">{e.name}</span>
                {#if e.repository}<span class="badge">git</span>{/if}
              </button>
              {#if e.repository}
                <button type="button" class="small" onclick={() => onpick(e.path ?? '')}>Choose</button>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </div>
    <footer class="foot">
      <span class="hint small">Open a directory by a click; a repository is chosen by its button.</span>
      <span class="grow"></span>
      <button type="button" class="ghost" onclick={onclose}>Cancel</button>
      <button type="button" class="primary" disabled={!here} onclick={() => onpick(here)}>Choose this directory</button>
    </footer>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 97;
    display: grid;
    place-items: center;
    background: rgb(0 0 0 / 0.4);
  }
  .panel {
    width: min(640px, calc(100vw - 32px));
    height: min(560px, calc(100vh - 32px));
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
    outline: none;
  }
  .head,
  .foot {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0.6rem 1rem;
    background: var(--chrome-2);
  }
  .head {
    border-bottom: 1px solid var(--border);
  }
  .foot {
    border-top: 1px solid var(--border);
  }
  .head h2 {
    margin: 0;
    font-size: 1.05em;
  }
  .grow {
    flex: 1;
  }
  .where {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0.5rem 1rem;
    border-bottom: 1px solid var(--border);
    min-width: 0;
  }
  .path {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    direction: rtl;
    text-align: left;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.4rem 0.6rem;
  }
  .entries {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .entries.dim {
    opacity: 0.6;
  }
  .entries li {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .entry {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    padding: 0.3rem 0.4rem;
    border: none;
    background: none;
    color: inherit;
    text-align: left;
    cursor: pointer;
    border-radius: var(--radius);
  }
  .entry:hover {
    background: var(--hover, var(--chrome-2));
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .badge {
    font-size: 0.75em;
    padding: 0 0.4em;
    border: 1px solid var(--border);
    border-radius: 999px;
    color: var(--muted);
  }
</style>
