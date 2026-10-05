<script lang="ts">
  // "Projects" tool: the ProjectUnit hierarchy of the `organisation` namespace (ADR 0039), mirroring the
  // Organisation tool's OrgUnit hierarchy (child --project_part_of--> parent). Projects are edited through
  // changes like any node. Right-clicking a project offers "New assignment" (ADR 0039: Assignment is
  // reachable from Organisation, Project or User, via an action or a context menu).
  import { types as nodeTypes, links as linkTypes, ns, rootProject } from '../../stores/session.svelte';
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import { tick } from 'svelte';
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { baselines, refreshBaselines } from '../../stores/catalog.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openContextMenu } from '../../shell/contextMenuState.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import { graph, errorMessage, nodeTitle, type GraphNode, type Link } from '../../api';


  let nodes = $state<GraphNode[]>([]);
  let links = $state<Link[]>([]);
  let loading = $state(false);
  let error = $state('');
  let adding = $state(false);
  let name = $state('');
  let kind = $state('project');
  let parent = $state('');
  let saving = $state(false);

  async function load() {
    loading = true;
    try {
      await refreshBaselines(ns.organisation);
      const latest = baselines.items[baselines.items.length - 1];
      if (!latest?.id) {
        nodes = [];
        links = [];
      } else {
        const r = await graph.getBaselineGraph(latest.id);
        nodes = (r.nodes ?? []).filter((n) => n.type === nodeTypes.projectUnit);
        links = r.links ?? [];
      }
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void stamp(keyOf.namespace(ns.organisation));
    void load();
  });

  const parentOf = $derived.by(() => {
    const m = new Map<string, string>();
    for (const l of links) if (l.type === linkTypes.projectPartOf && l.from?.id && l.to?.id && l.from.id !== l.to.id) m.set(l.from.id, l.to.id);
    return m;
  });
  const children = $derived.by(() => {
    const m = new Map<string, GraphNode[]>();
    for (const n of nodes) {
      const p = parentOf.get(n.id ?? '') ?? '';
      m.set(p, [...(m.get(p) ?? []), n]);
    }
    for (const list of m.values()) list.sort((a, b) => (a.key ?? '').localeCompare(b.key ?? ''));
    return m;
  });
  const known = $derived(new Set(nodes.map((n) => n.id ?? '')));
  const roots = $derived(nodes.filter((n) => !known.has(parentOf.get(n.id ?? '') ?? '')).sort((a, b) => (a.key ?? '').localeCompare(b.key ?? '')));

  function label(n: GraphNode): string {
    return nodeTitle(n) || n.key || '';
  }

  /** a node's own id and every id under it, to keep "Move to…" from creating a cycle. */
  function subtreeOf(n: GraphNode): Set<string> {
    const out = new Set<string>([n.id ?? '']);
    const stack = [n.id ?? ''];
    while (stack.length) {
      const id = stack.pop()!;
      for (const k of children.get(id) ?? []) {
        if (!out.has(k.id ?? '')) {
          out.add(k.id ?? '');
          stack.push(k.id ?? '');
        }
      }
    }
    return out;
  }

  function open(n: GraphNode, pin = false, openAssignment = false) {
    openTab({ kind: 'project', params: { key: n.key ?? '', ...(openAssignment ? { pane: 'assignments', newAssignment: '1' } : {}) } }, { pin });
  }

  function slug(s: string): string {
    return s
      .normalize('NFD')
      .replace(/[^\w]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .toUpperCase();
  }

  async function create() {
    const latest = baselines.items[baselines.items.length - 1];
    if (!latest?.id || !name.trim()) return;
    saving = true;
    error = '';
    try {
      const key = `PROJ-${slug(name)}`;
      const parentNode = nodes.find((n) => n.id === parent);
      await graph.commitEdits({
        title: `Project ${key}`,
        intent: `Create project ${name.trim()}`,
        baselineId: latest.id,
        namespace: ns.organisation,
        edits: [
          {
            key,
            type: nodeTypes.projectUnit,
            props: { name: name.trim(), kind, status: 'active' },
            rationale: `Create project ${name.trim()}`,
            ...(parentNode ? { links: [{ type: linkTypes.projectPartOf, to: { id: parentNode.id, version: parentNode.version } }] } : {}),
          },
        ],
      });
      notify(`Project ${key} created.`, 'ok');
      name = '';
      parent = '';
      adding = false;
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }

  async function createChild(n: GraphNode) {
    adding = true;
    moving = undefined;
    parent = n.id ?? '';
    await tick();
    document.getElementById('proj-new-name')?.focus();
  }

  let moving = $state<GraphNode>();
  let moveTarget = $state('');
  let movingBusy = $state(false);

  /** projects `n` can move under: not itself, and not one of its own sub-projects (would create a cycle). */
  const moveCandidates = $derived.by(() => {
    if (!moving) return [];
    const excluded = subtreeOf(moving);
    return nodes.filter((n) => !excluded.has(n.id ?? '')).sort((a, b) => (a.key ?? '').localeCompare(b.key ?? ''));
  });

  function startMove(n: GraphNode) {
    moving = n;
    moveTarget = '';
    adding = false;
  }

  async function move() {
    if (!moving || !moveTarget) return;
    const target = nodes.find((n) => n.id === moveTarget);
    const latest = baselines.items[baselines.items.length - 1];
    if (!target || !latest?.id) return;
    movingBusy = true;
    error = '';
    try {
      const current = links.find((l) => l.type === linkTypes.projectPartOf && l.from?.id === moving!.id && l.to?.id !== moving!.id);
      await graph.commitEdits({
        title: `Move ${moving.key}`,
        intent: `Move ${moving.key} under ${target.key}`,
        baselineId: latest.id,
        namespace: ns.organisation,
        edits: [
          {
            pre: { id: moving.id, version: moving.version },
            rationale: `Move ${moving.key} under ${target.key}`,
            ...(current?.id ? { removeLinks: [current.id] } : {}),
            links: [{ type: linkTypes.projectPartOf, to: { id: target.id, version: target.version } }],
          },
        ],
      });
      notify(`${moving.key} moved under ${target.key}.`, 'ok');
      moving = undefined;
      moveTarget = '';
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      movingBusy = false;
    }
  }
</script>

{#snippet branch(n: GraphNode, depth: number)}
  {@const kids = children.get(n.id ?? '') ?? []}
  <TreeRow
    {depth}
    icon={kids.length ? 'folder' : 'diff'}
    label={label(n)}
    detail={n.key}
    expanded={kids.length ? isOpen(`proj:${n.id}`, true) : undefined}
    ontoggle={() => toggle(`proj:${n.id}`, true)}
    onselect={() => open(n)}
    onopen={() => open(n, true)}
    badge={typeof n.props?.['kind'] === 'string' ? (n.props['kind'] as string) : undefined}
    oncontextmenu={(e) =>
      openContextMenu(e, [
        { label: 'Open', icon: 'diff', run: () => open(n, true) },
        { label: 'New sub-project…', icon: 'plus', run: () => createChild(n) },
        ...(n.key !== rootProject() ? [{ label: 'Move to…', icon: 'folder' as const, run: () => startMove(n) }] : []),
        { label: 'New assignment', icon: 'plus', run: () => open(n, true, true) },
      ])}
  />
  {#if kids.length && isOpen(`proj:${n.id}`, true)}
    {#each kids as k (k.id)}{@render branch(k, depth + 1)}{/each}
  {/if}
{/snippet}

<div class="explorer">
  <div class="tools">
    <button type="button" class="small" onclick={() => (adding = !adding)}><Icon name="plus" size={13} /> New project</button>
    <span class="grow"></span>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if adding}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void create();
      }}
    >
      <input id="proj-new-name" placeholder="Name" bind:value={name} required />
      <select bind:value={kind} aria-label="Kind">
        {#each ['program', 'project', 'subproject'] as k (k)}<option value={k}>{k}</option>{/each}
      </select>
      <select bind:value={parent} aria-label="Parent project">
        <option value="">No parent</option>
        {#each nodes as n (n.id)}<option value={n.id}>{label(n)}</option>{/each}
      </select>
      <button type="submit" class="small" disabled={saving || !name.trim()}>Create</button>
    </form>
  {/if}
  {#if moving}
    <div class="form move">
      <span class="hint">Move <code>{moving.key}</code> to…</span>
      <select bind:value={moveTarget} disabled={movingBusy} aria-label="New parent project">
        <option value="">Pick a project…</option>
        {#each moveCandidates as o (o.id)}<option value={o.id}>{label(o)}</option>{/each}
      </select>
      <div class="row">
        <button type="button" class="small primary" disabled={!moveTarget || movingBusy} onclick={move}>Move</button>
        <button type="button" class="small" disabled={movingBusy} onclick={() => (moving = undefined)}>Cancel</button>
      </div>
    </div>
  {/if}
  {#if error}<div class="alert small">{error}</div>{/if}
  <div role="tree" aria-label="Projects">
    {#each roots as n (n.id)}{@render branch(n, 0)}{:else}
      {#if !loading && !error}<p class="empty pad">No project.</p>{/if}
    {/each}
  </div>
</div>

<style>
  .tools {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
  }
  .tools button {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    padding: 0 0.5rem 0.5rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
  .form.move {
    background: var(--panel-alt, #8881);
    border-radius: 4px;
    margin: 0 0.5rem 0.5rem;
    padding: 0.4rem 0.5rem;
  }
</style>
