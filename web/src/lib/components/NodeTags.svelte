<script lang="ts">
  // The tags and the smart tags of a node (ADR 0143 §6, §7): a tag names one exact version of it, a smart tag is a
  // shared selection of exact versions this node may be part of. Neither changes the node: tagging a version and
  // editing a selection go through no change.
  import { graph, smartTags, errorMessage, formatDate, type SmartTag, type Tag } from '../api';
  import { notify } from '../shell/workbench.svelte';

  let { id, versions }: { id: string; /** the versions of the node, oldest first */ versions: number[] } = $props();

  let tags = $state<Tag[]>([]);
  let smart = $state<SmartTag[]>([]);
  let mine = $state<SmartTag[]>([]);
  let error = $state('');
  let busy = $state(false);

  let tagName = $state('');
  let tagVersion = $state(0);
  let addTo = $state('');
  let addVersion = $state(0);
  let newName = $state('');

  const latest = $derived(versions.at(-1) ?? 0);
  $effect(() => {
    if (!tagVersion || !versions.includes(tagVersion)) tagVersion = latest;
    if (!addVersion || !versions.includes(addVersion)) addVersion = latest;
  });

  async function load(signal?: AbortSignal) {
    error = '';
    try {
      const [t, s, all] = await Promise.all([graph.listTags({ nodeId: id }), smartTags.list(id, signal), smartTags.list('', signal)]);
      tags = t.tags ?? [];
      smart = s.smartTags ?? [];
      mine = (all.smartTags ?? []).filter((x) => x.access === 'owner' || x.access === 'edit');
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    }
  }

  $effect(() => {
    void id;
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  });

  async function act(what: () => Promise<unknown>, done: string) {
    busy = true;
    try {
      await what();
      notify(done, 'ok');
      await load();
    } catch (e) {
      notify(errorMessage(e), 'error');
    } finally {
      busy = false;
    }
  }

  function tag() {
    const name = tagName.trim();
    if (!name || !tagVersion) return;
    void act(() => graph.tagVersion(id, tagVersion, name), `v${tagVersion} tagged ${name}`).then(() => (tagName = ''));
  }

  function addToSmart() {
    if (!addVersion) return;
    const version = { nodeId: id, version: addVersion };
    if (addTo === 'new') {
      const name = newName.trim();
      if (!name) return;
      void act(() => smartTags.create(name, '', [version]), `smart tag ${name} created`).then(() => {
        newName = '';
        addTo = '';
      });
      return;
    }
    const target = mine.find((x) => x.id === addTo);
    if (!target) return;
    void act(() => smartTags.update(addTo, { add: [version] }), `v${addVersion} added to ${target.name}`);
  }

  const selected = (t: SmartTag) => (t.versions ?? []).filter((v) => v.nodeId === id).map((v) => v.version ?? 0);
</script>

<section class="card">
  <h3>Tags <span class="count">{tags.length}</span></h3>
  {#if error}<div class="alert">{error}</div>{/if}
  <p class="hint">A tag names one exact version; what it stands for is read from that version and the versions it links to.</p>
  {#if tags.length}
    <ul class="rows">
      {#each tags as t (t.id)}
        <li>
          <span class="name">{t.name}</span>
          <span class="ver">v{t.version}</span>
          <span class="hint">{t.by ? `${t.by} · ` : ''}{formatDate(t.createdAt)}</span>
          <button type="button" class="small" disabled={busy} title="Delete the tag (the version stays)" onclick={() => act(() => graph.deleteTag(t.id ?? ''), `tag ${t.name} deleted`)}>Delete</button>
        </li>
      {/each}
    </ul>
  {/if}
  {#if versions.length}
    <form class="row" onsubmit={(e) => (e.preventDefault(), tag())}>
      <label for="tag-version">Version</label>
      <select id="tag-version" bind:value={tagVersion}>
        {#each [...versions].reverse() as v (v)}<option value={v}>v{v}</option>{/each}
      </select>
      <input aria-label="Tag name" placeholder="release-1.2" bind:value={tagName} />
      <button type="submit" class="small" disabled={busy || !tagName.trim()}>Tag</button>
    </form>
  {/if}
</section>

<section class="card">
  <h3>Smart tags <span class="count">{smart.length}</span></h3>
  <p class="hint">A smart tag selects exact versions of any nodes to give people a context; its owner shares it to read or to edit.</p>
  {#if smart.length}
    <ul class="rows">
      {#each smart as t (t.id)}
        <li>
          <span class="name">{t.name}</span>
          {#each selected(t) as v (v)}
            <span class="ver">v{v}</span>
            {#if t.access !== 'read'}
              <button type="button" class="small" disabled={busy} title="Take this version out of the smart tag" onclick={() => act(() => smartTags.update(t.id ?? '', { remove: [{ nodeId: id, version: v }] }), `v${v} taken out of ${t.name}`)}>Remove v{v}</button>
            {/if}
          {/each}
          <span class="hint">{t.access === 'owner' ? 'yours' : `${t.owner}'s, ${t.access}`} · {(t.versions ?? []).length} version{(t.versions ?? []).length === 1 ? '' : 's'}</span>
        </li>
      {/each}
    </ul>
  {/if}
  {#if versions.length}
    <form class="row" onsubmit={(e) => (e.preventDefault(), addToSmart())}>
      <label for="smart-version">Add</label>
      <select id="smart-version" bind:value={addVersion}>
        {#each [...versions].reverse() as v (v)}<option value={v}>v{v}</option>{/each}
      </select>
      <label for="smart-target">to</label>
      <select id="smart-target" bind:value={addTo}>
        <option value="" disabled>a smart tag…</option>
        {#each mine as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
        <option value="new">New smart tag…</option>
      </select>
      {#if addTo === 'new'}<input aria-label="Smart tag name" placeholder="Name" bind:value={newName} />{/if}
      <button type="submit" class="small" disabled={busy || !addTo || (addTo === 'new' && !newName.trim())}>Add</button>
    </form>
  {/if}
</section>

<style>
  .rows {
    list-style: none;
    margin: 0 0 0.5rem;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .rows li,
  .row {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .name {
    font-weight: 500;
  }
  .ver {
    font-family: var(--mono);
    font-size: 0.85rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.45rem;
  }
  .row select,
  .row input {
    width: auto;
    min-width: 8rem;
  }
</style>
