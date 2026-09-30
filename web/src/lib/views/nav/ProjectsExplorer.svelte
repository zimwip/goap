<script lang="ts">
  // "Projects" tool: the ProjectUnit hierarchy of the `organisation` namespace (ADR 0039), mirroring the
  // Organisation tool's OrgUnit hierarchy (child --project_part_of--> parent). Projects are edited through
  // changes like any node. Right-clicking a project offers "New assignment" (ADR 0039: Assignment is
  // reachable from Organisation, Project or User, via an action or a context menu).
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { baselines, refreshBaselines } from '../../stores/catalog.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openContextMenu } from '../../shell/contextMenuState.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import { graph, errorMessage, nodeTitle, type GraphNode, type Link } from '../../api';
  import { PROJECT_UNIT_TYPE, PROJECT_PART_OF } from '../../orgTypes';

  const NS = 'organisation';

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
      await refreshBaselines(NS);
      const latest = baselines.items[baselines.items.length - 1];
      if (!latest?.id) {
        nodes = [];
        links = [];
      } else {
        const r = await graph.getBaselineGraph(latest.id);
        nodes = (r.nodes ?? []).filter((n) => n.type === PROJECT_UNIT_TYPE);
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
    void load();
  });

  const parentOf = $derived.by(() => {
    const m = new Map<string, string>();
    for (const l of links) if (l.type === PROJECT_PART_OF && l.from?.id && l.to?.id && l.from.id !== l.to.id) m.set(l.from.id, l.to.id);
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
        namespace: NS,
        edits: [
          {
            key,
            type: PROJECT_UNIT_TYPE,
            props: { name: name.trim(), kind, status: 'active' },
            rationale: `Create project ${name.trim()}`,
            ...(parentNode ? { links: [{ type: PROJECT_PART_OF, to: { id: parentNode.id, version: parentNode.version } }] } : {}),
          },
        ],
      });
      notify(`Project ${key} created.`, 'ok');
      name = '';
      parent = '';
      adding = false;
      await refreshBaselines(NS);
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
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
      <input placeholder="Name" bind:value={name} required />
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
</style>
