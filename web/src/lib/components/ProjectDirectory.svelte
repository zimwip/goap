<script lang="ts">
  // The directory of a project (ADR 0178): the trees of its smart tags as folders, which its members read and edit.
  // A folder holds nodes of any namespace, each followed in the view (the node itself) or kept at one version. Folders
  // are moved by dragging them onto another folder (or onto the top), nodes are added by dropping them on a folder: a
  // link to a node, a result of a query, the title of a node editor, an entry of another folder. Nothing here writes to
  // the graph: a folder is a smart tag, a move an edit of smart tags.
  import { graph, smartTags, errorMessage, type GraphNode, type SmartTag, type TaggedVersion } from '../api';
  import { notify } from '../shell/workbench.svelte';
  import { confirmDialog } from '../shell/confirmState.svelte';
  import { openNode } from '../nodeEditors';
  import { contextView, graphView } from '../contextView';
  import { namespaceOf } from '../namespace';
  import { carriesNode, dragNode, droppedNode } from '../nodeDrag';
  import { buildTree, canMoveFolder, entryKey, entryOf, follows, holds, visible } from '../projectDirectory';

  let { project }: { project: string } = $props();

  /** what a dragged folder carries: its id */
  const FOLDER_DRAG = 'application/x-goap-folder';
  /** what a dragged entry carries: the folder it leaves and the entry */
  const ENTRY_DRAG = 'application/x-goap-folder-entry';

  let tags = $state<SmartTag[]>([]);
  let error = $state('');
  let loading = $state(false);
  let busy = $state(false);
  let selected = $state('');
  let opened = $state<Record<string, boolean>>({});
  /** a node dropped follows the node (the view's version), else keeps the version it was shown at */
  let follow = $state(true);
  let over = $state(''); // the folder a drag is over ('' none, '\u0000top' the top level)
  let newName = $state('');
  let renaming = $state('');
  let renameTo = $state('');

  const roots = $derived(buildTree(tags));
  const rows = $derived(visible(roots, (id) => opened[id] ?? false));
  const current = $derived(tags.find((t) => t.id === selected));

  async function load(signal?: AbortSignal) {
    loading = true;
    error = '';
    try {
      tags = (await smartTags.ofProject(project, signal)).smartTags ?? [];
      if (selected && !tags.some((t) => t.id === selected)) selected = '';
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void project;
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  });

  async function act(what: () => Promise<unknown>, done?: string): Promise<boolean> {
    busy = true;
    try {
      await what();
      if (done) notify(done, 'ok');
      await load();
      return true;
    } catch (e) {
      notify(errorMessage(e), 'error');
      return false;
    } finally {
      busy = false;
    }
  }

  function create(parent: string) {
    const name = newName.trim();
    if (!name) return;
    void act(async () => {
      const t = (await smartTags.create(name, '', [], parent ? { parent } : { project })).smartTag;
      if (parent) opened[parent] = true;
      selected = t.id ?? '';
    }, `Folder ${name} created`).then((ok) => ok && (newName = ''));
  }

  function rename(t: SmartTag) {
    const name = renameTo.trim();
    renaming = '';
    if (!name || name === t.name) return;
    void act(() => smartTags.update(t.id ?? '', { name }), `Folder renamed ${name}`);
  }

  async function remove(t: SmartTag) {
    if (!(await confirmDialog({ message: `Delete the folder ${t.name}? The nodes it lists are not touched.`, danger: true }))) return;
    void act(() => smartTags.remove(t.id ?? ''), `Folder ${t.name} deleted`);
  }

  // ---- drag and drop ------------------------------------------------------------

  const TOP = '\u0000top';
  const accepts = (e: DragEvent) => !!e.dataTransfer && (e.dataTransfer.types.includes(FOLDER_DRAG) || e.dataTransfer.types.includes(ENTRY_DRAG) || carriesNode(e));

  function dragOver(e: DragEvent, target: string) {
    if (!accepts(e)) return;
    // a node or an entry goes into a folder, not onto the top level
    if (target === TOP && !e.dataTransfer!.types.includes(FOLDER_DRAG)) return;
    e.preventDefault();
    e.dataTransfer!.dropEffect = e.dataTransfer!.types.includes(FOLDER_DRAG) || e.dataTransfer!.types.includes(ENTRY_DRAG) ? 'move' : 'copy';
    if (over !== target) unfoldLater(target);
    over = target;
  }

  // a closed folder held under a drag unfolds after a moment, so a drop can reach its sub-folders
  let unfolding: ReturnType<typeof setTimeout> | undefined;
  function unfoldLater(target: string) {
    clearTimeout(unfolding);
    if (target === TOP || opened[target]) return;
    unfolding = setTimeout(() => over === target && (opened[target] = true), 600);
  }

  function drop(e: DragEvent, target: string) {
    e.preventDefault();
    over = '';
    const dt = e.dataTransfer;
    if (!dt) return;
    const parent = target === TOP ? '' : target;
    const folder = dt.getData(FOLDER_DRAG);
    if (folder) {
      if (!canMoveFolder(tags, folder, parent)) return;
      const t = tags.find((x) => x.id === folder);
      void act(() => smartTags.update(folder, { parent }), `${t?.name ?? 'Folder'} moved`).then((ok) => ok && parent && (opened[parent] = true));
      return;
    }
    if (!parent) return;
    const to = tags.find((x) => x.id === parent);
    if (!to) return;
    const moved = dt.getData(ENTRY_DRAG);
    if (moved) {
      const { from, entry } = JSON.parse(moved) as { from: string; entry: TaggedVersion };
      if (from === parent) return;
      void act(async () => {
        if (!holds(to, entry)) await smartTags.update(parent, { add: [entry] });
        await smartTags.update(from, { remove: [entry] });
      }, `Moved to ${to.name}`);
      return;
    }
    const n = droppedNode(e);
    if (!n) return;
    const entry = entryOf(n, follow || !n.version);
    if (holds(to, entry)) return notify(`${to.name} holds ${n.key ?? n.id} already`, 'info');
    if (n.key) labels[entryKey(entry)] = { key: n.key, type: n.type ?? '', namespace: n.namespace ?? '' };
    void act(() => smartTags.update(parent, { add: [entry] }), `${n.key ?? n.id} added to ${to.name}`);
  }

  // ---- the entries of the folder selected ---------------------------------------

  type Shown = { key: string; type: string; namespace: string; version?: number; gone?: boolean; node?: GraphNode };
  let labels = $state<Record<string, Shown>>({});

  /** reads an entry: an exact version as it was written, the node itself as the view of its namespace holds it */
  async function read(e: TaggedVersion, signal: AbortSignal): Promise<Shown> {
    const id = e.nodeId ?? '';
    if (!follows(e)) {
      const n = (await graph.getNode({ id, version: e.version ?? 0 }, signal)).view?.node;
      return { key: n?.key ?? id, type: n?.type ?? '', namespace: n?.namespace ?? '', version: n?.version, node: n };
    }
    const latest = (await graph.listNodeVersions(id, signal, { limit: 1 })).versions?.[0];
    const namespace = namespaceOf(latest?.namespace ?? '');
    try {
      const n = (await graph.getNode({ id, version: 0 }, signal, graphView(await contextView(namespace, signal)), 1)).view?.node;
      return { key: n?.key ?? latest?.key ?? id, type: n?.type ?? latest?.type ?? '', namespace, version: n?.version, gone: !n || n.deleted, node: n };
    } catch {
      return { key: latest?.key ?? id, type: latest?.type ?? '', namespace, gone: true, node: latest };
    }
  }

  $effect(() => {
    const entries = current?.versions ?? [];
    const ctrl = new AbortController();
    for (const e of entries) {
      const k = entryKey(e);
      if (labels[k]?.node) continue;
      void read(e, ctrl.signal)
        .then((s) => (labels[k] = s))
        .catch(() => {
          if (!ctrl.signal.aborted) labels[k] = { key: e.nodeId ?? '', type: '', namespace: '', gone: true };
        });
    }
    return () => ctrl.abort();
  });

  function setFollow(e: TaggedVersion, on: boolean) {
    const s = labels[entryKey(e)];
    if (!current || (!on && !s?.version)) return;
    const next: TaggedVersion = { nodeId: e.nodeId, version: on ? 0 : s!.version };
    void act(async () => {
      if (!holds(current, next)) await smartTags.update(current.id ?? '', { add: [next] });
      await smartTags.update(current.id ?? '', { remove: [e] });
    });
  }
</script>

<section class="card">
  <h3>Directory</h3>
  <p class="hint">
    Folders to arrange the nodes of the project, of any namespace, without changing them. Drag a folder onto another to move it, drop a node
    on a folder to add it: a link to a node, a result of a query, the title of a node editor, an entry of another folder.
  </p>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="bar">
    <input type="text" placeholder="Folder name" bind:value={newName} aria-label="Folder name" onkeydown={(e) => e.key === 'Enter' && create(selected)} />
    <button type="button" class="small" disabled={busy || !newName.trim()} onclick={() => create('')}>New folder</button>
    <button type="button" class="small" disabled={busy || !newName.trim() || !current} onclick={() => create(selected)} title={current ? `Under ${current.name}` : 'Select a folder first'}>New sub-folder</button>
    <label class="inline" title="A dropped node either follows the node (read at the version the view holds) or keeps the version it was shown at">
      <input type="checkbox" bind:checked={follow} /> dropped nodes follow their latest version
    </label>
  </div>
  <div class="cols">
    <div class="tree" role="tree" aria-label="Folders of {project}">
      {#each rows as f (f.tag.id)}
        {@const id = f.tag.id ?? ''}
        <div
          class="folder"
          class:sel={selected === id}
          class:over={over === id}
          role="treeitem"
          aria-selected={selected === id}
          aria-expanded={f.children.length ? (opened[id] ?? false) : undefined}
          tabindex="0"
          style="padding-left: {0.25 + f.depth * 1.1}rem"
          draggable="true"
          ondragstart={(e) => e.dataTransfer?.setData(FOLDER_DRAG, id)}
          ondragover={(e) => dragOver(e, id)}
          ondragleave={() => over === id && (over = '')}
          ondrop={(e) => drop(e, id)}
          onclick={() => (selected = id)}
          onkeydown={(e) => e.key === 'Enter' && (selected = id)}
        >
          <button type="button" class="twist" aria-label={opened[id] ? 'Fold' : 'Unfold'} disabled={!f.children.length}
            onclick={(e) => { e.stopPropagation(); opened[id] = !opened[id]; }}>{f.children.length ? (opened[id] ? '▾' : '▸') : '·'}</button>
          {#if renaming === id}
            <!-- svelte-ignore a11y_autofocus -->
            <input type="text" bind:value={renameTo} autofocus aria-label="New name" onclick={(e) => e.stopPropagation()}
              onkeydown={(e) => { if (e.key === 'Enter') rename(f.tag); if (e.key === 'Escape') renaming = ''; }} onblur={() => rename(f.tag)} />
          {:else}
            <span class="name">{f.tag.name}</span>
            <span class="count">{f.tag.versions?.length ?? 0}</span>
          {/if}
        </div>
      {:else}
        {#if !loading}<p class="empty">No folder yet: name one and press <em>New folder</em>.</p>{/if}
      {/each}
      {#if tags.length}
        <div class="top" class:over={over === TOP} role="region" aria-label="Top level" ondragover={(e) => dragOver(e, TOP)} ondragleave={() => over === TOP && (over = '')} ondrop={(e) => drop(e, TOP)}>
          Drop a folder here to move it to the top
        </div>
      {/if}
    </div>
    <div class="entries">
      {#if current}
        <div class="head">
          <strong>{current.name}</strong>
          <span class="hint">by {current.owner}</span>
          <span class="grow"></span>
          <button type="button" class="small" disabled={busy} onclick={() => { renaming = current.id ?? ''; renameTo = current.name ?? ''; }}>Rename</button>
          <button type="button" class="small danger" disabled={busy} onclick={() => remove(current)}>Delete</button>
        </div>
        {#if current.versions?.length}
          <table class="tbl">
            <thead><tr><th>Node</th><th>Type</th><th>Version</th><th></th></tr></thead>
            <tbody>
              {#each current.versions as e (entryKey(e))}
                {@const s = labels[entryKey(e)]}
                <tr class:gone={s?.gone}>
                  <td>
                    <button type="button" class="link mono" draggable="true" disabled={!s?.node}
                      ondragstart={(ev) => { ev.dataTransfer?.setData(ENTRY_DRAG, JSON.stringify({ from: current.id, entry: e })); dragNode(ev, s?.node ? { ...s.node, version: e.version ?? 0 } : undefined); }}
                      onclick={() => s?.node && void openNode(s.node, { pin: true })}>{s?.key ?? e.nodeId}</button>
                  </td>
                  <td class="hint">{s?.type ?? ''}</td>
                  <td>
                    {#if follows(e)}
                      <span title="Follows the node: the version the view holds">follows{s?.version ? ` · v${s.version}` : ''}{s?.gone ? ' · gone' : ''}</span>
                    {:else}
                      <span title="Pinned at this version">v{e.version}</span>
                    {/if}
                  </td>
                  <td class="act">
                    {#if follows(e)}
                      <button type="button" class="small" disabled={busy || !s?.version} onclick={() => setFollow(e, false)} title="Keep the version the view holds now">Pin</button>
                    {:else}
                      <button type="button" class="small" disabled={busy} onclick={() => setFollow(e, true)} title="Follow the node instead of this version">Follow</button>
                    {/if}
                    <button type="button" class="small danger" disabled={busy} onclick={() => void act(() => smartTags.update(current.id ?? '', { remove: [e] }))}>Remove</button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {:else}
          <p class="empty">Empty: drop a node here, or on the folder.</p>
        {/if}
        <div class="drop" class:over={over === current.id} role="region" aria-label="Drop a node into {current.name}"
          ondragover={(e) => dragOver(e, current.id ?? '')} ondragleave={() => over === current.id && (over = '')} ondrop={(e) => drop(e, current.id ?? '')}>
          Drop a node here
        </div>
      {:else}
        <p class="empty">Select a folder to see what it holds.</p>
      {/if}
    </div>
  </div>
</section>

<style>
  .bar {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: 0.5rem;
  }
  .cols {
    display: grid;
    grid-template-columns: minmax(12rem, 1fr) 2fr;
    gap: 0.75rem;
  }
  @media (max-width: 720px) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
  .tree {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm, 4px);
    padding: 0.25rem;
    min-height: 6rem;
  }
  .folder {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.15rem 0.25rem;
    border-radius: var(--radius-sm, 4px);
    cursor: pointer;
  }
  .folder:hover {
    background: var(--hover);
  }
  .folder.sel {
    background: var(--accent-soft);
  }
  .over {
    outline: 2px dashed var(--accent);
    outline-offset: -2px;
  }
  .twist {
    border: none;
    background: none;
    padding: 0 0.15rem;
    width: 1.2rem;
    color: var(--muted);
  }
  .name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .count {
    color: var(--muted);
    font-size: 0.8em;
  }
  .top,
  .drop {
    margin-top: 0.5rem;
    padding: 0.4rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius-sm, 4px);
    color: var(--muted);
    font-size: 0.85em;
    text-align: center;
  }
  .entries .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }
  .grow {
    flex: 1;
  }
  .tbl {
    width: 100%;
    border-collapse: collapse;
  }
  .tbl th,
  .tbl td {
    text-align: left;
    padding: 0.25rem 0.5rem;
    border-bottom: 1px solid var(--border);
  }
  .tbl td.act {
    white-space: nowrap;
    text-align: right;
  }
  tr.gone td {
    color: var(--muted);
    text-decoration: line-through;
  }
</style>
