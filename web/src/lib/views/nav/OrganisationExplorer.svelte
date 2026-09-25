<script lang="ts">
  // "Organisation" tool: the OrgUnit hierarchy of the `organisation` namespace
  // (child --part_of--> parent). Units are edited through changes like any node.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { baselines, refreshBaselines } from '../../stores/catalog.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import { graph, errorMessage, nodeTitle, type GraphNode, type Link } from '../../api';

  const NS = 'organisation';

  let nodes = $state<GraphNode[]>([]);
  let links = $state<Link[]>([]);
  let loading = $state(false);
  let error = $state('');
  let adding = $state(false);
  let name = $state('');
  let kind = $state('team');
  let parent = $state('');
  let saving = $state(false);

  async function load() {
    loading = true;
    try {
      if (!baselines.loaded) await refreshBaselines();
      const latest = baselines.items[baselines.items.length - 1];
      if (!latest?.id) {
        nodes = [];
        links = [];
      } else {
        const r = await graph.getBaselineGraph(latest.id);
        nodes = (r.nodes ?? []).filter((n) => n.namespace === NS && n.type === 'OrgUnit');
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
    for (const l of links) if (l.type === 'part_of' && l.from?.id && l.to?.id) m.set(l.from.id, l.to.id);
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

  function open(n: GraphNode, pin = false) {
    openTab({ kind: 'node', params: { id: n.id ?? '', key: n.key ?? '' } }, { pin });
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
      const key = `ORG-${slug(name)}`;
      const { change } = await graph.createChange({
        title: `New unit ${name.trim()}`,
        intent: `Create organisational unit ${name.trim()}`,
        baselineId: latest.id,
        namespace: NS,
      });
      if (!change?.id) throw new Error('change not created');
      const itemId = crypto.randomUUID();
      const parentNode = nodes.find((n) => n.id === parent);
      await graph.addItems(change.id, [
        {
          id: itemId,
          kind: 'proposal',
          type: 'orgunit',
          proposal: { op: 'create_node', node: { key, type: 'OrgUnit', props: { name: name.trim(), kind } } },
        },
        ...(parentNode
          ? [
              {
                kind: 'proposal',
                type: 'orgunit',
                proposal: {
                  op: 'add_link',
                  link: { type: 'part_of', from: { item: itemId }, to: { node: { id: parentNode.id, version: parentNode.version } } },
                },
              },
            ]
          : []),
      ]);
      await graph.applyChange(change.id, `Unit ${key}`);
      notify(`Unit ${key} created.`, 'ok');
      name = '';
      parent = '';
      adding = false;
      await refreshBaselines();
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
    icon={kids.length ? 'folder' : 'user'}
    label={label(n)}
    detail={n.key}
    expanded={kids.length ? isOpen(`org:${n.id}`, true) : undefined}
    ontoggle={() => toggle(`org:${n.id}`, true)}
    onselect={() => open(n)}
    onopen={() => open(n, true)}
    badge={typeof n.props?.['kind'] === 'string' ? (n.props['kind'] as string) : undefined}
  />
  {#if kids.length && isOpen(`org:${n.id}`, true)}
    {#each kids as k (k.id)}{@render branch(k, depth + 1)}{/each}
  {/if}
{/snippet}

<div class="explorer">
  <div class="tools">
    <button type="button" class="small" onclick={() => (adding = !adding)}><Icon name="plus" size={13} /> New unit</button>
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
        {#each ['company', 'direction', 'department', 'team'] as k (k)}<option value={k}>{k}</option>{/each}
      </select>
      <select bind:value={parent} aria-label="Parent unit">
        <option value="">No parent</option>
        {#each nodes as n (n.id)}<option value={n.id}>{label(n)}</option>{/each}
      </select>
      <button type="submit" class="small" disabled={saving || !name.trim()}>Create</button>
    </form>
  {/if}
  {#if error}<div class="alert small">{error}</div>{/if}
  <div role="tree" aria-label="Organisation">
    {#each roots as n (n.id)}{@render branch(n, 0)}{:else}
      {#if !loading && !error}<p class="empty pad">No organisational units.</p>{/if}
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
