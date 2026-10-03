<script lang="ts">
  // "Users" tool: the User nodes of the `organisation` namespace (ADR 0039/0020). A user is created
  // automatically the first time they are seen (no administrator has to create them by hand, see
  // internal/graphsvc.EnsureUser); this list is where they show up, and where one can be pre-provisioned
  // (given roles before they ever sign in) or right-clicked for "New assignment" (ADR 0039).
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { baselines, refreshBaselines } from '../../stores/catalog.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openContextMenu } from '../../shell/contextMenuState.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import { graph, errorMessage, nodeTitle, type GraphNode } from '../../api';
  import { ORG_UNIT_TYPE, USER_TYPE, MEMBER_OF, newUserUnit } from '../../orgTypes';

  const NS = 'organisation';

  let nodes = $state<GraphNode[]>([]);
  let units = $state<GraphNode[]>([]);
  let loading = $state(false);
  let error = $state('');
  let adding = $state(false);
  let subject = $state('');
  let saving = $state(false);

  async function load() {
    loading = true;
    try {
      await refreshBaselines(NS);
      const latest = baselines.items[baselines.items.length - 1];
      if (!latest?.id) {
        nodes = [];
        units = [];
      } else {
        const r = await graph.getBaselineGraph(latest.id);
        nodes = (r.nodes ?? []).filter((n) => n.type === USER_TYPE).sort((a, b) => (a.key ?? '').localeCompare(b.key ?? ''));
        units = (r.nodes ?? []).filter((n) => n.type === ORG_UNIT_TYPE);
      }
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void stamp(keyOf.namespace(NS));
    void load();
  });

  function label(n: GraphNode): string {
    return nodeTitle(n) || (typeof n.props?.['subject'] === 'string' ? (n.props['subject'] as string) : '') || n.key || '';
  }

  function open(n: GraphNode, pin = false, openAssignment = false) {
    openTab({ kind: 'user', params: { key: n.key ?? '', ...(openAssignment ? { pane: 'assignments', newAssignment: '1' } : {}) } }, { pin });
  }

  async function create() {
    const latest = baselines.items[baselines.items.length - 1];
    const s = subject.trim();
    if (!latest?.id || !s) return;
    saving = true;
    error = '';
    try {
      const key = `USR:${s}`;
      const unit = units.find((n) => n.key === newUserUnit(units));
      if (!unit?.id) throw new Error('no organisation unit to put the user in');
      await graph.commitEdits({
        title: `User ${s}`,
        intent: `Pre-provision the user ${s}`,
        baselineId: latest.id,
        namespace: NS,
        edits: [{ key, type: USER_TYPE, props: { subject: s }, links: [{ type: MEMBER_OF, to: { id: unit.id, version: unit.version } }], rationale: `Pre-provision the user ${s}` }],
      });
      notify(`User ${s} created.`, 'ok');
      subject = '';
      adding = false;
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="explorer">
  <div class="tools">
    <button type="button" class="small" onclick={() => (adding = !adding)}><Icon name="plus" size={13} /> New user</button>
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
      <input placeholder="Subject (login)" bind:value={subject} required />
      <button type="submit" class="small" disabled={saving || !subject.trim()}>Create</button>
    </form>
  {/if}
  {#if error}<div class="alert small">{error}</div>{/if}
  <div role="tree" aria-label="Users">
    {#each nodes as n (n.id)}
      <TreeRow
        depth={0}
        icon="user"
        label={label(n)}
        detail={n.key}
        onselect={() => open(n)}
        onopen={() => open(n, true)}
        oncontextmenu={(e) =>
          openContextMenu(e, [
            { label: 'Open', icon: 'user', run: () => open(n, true) },
            { label: 'New assignment', icon: 'plus', run: () => open(n, true, true) },
          ])}
      />
    {:else}
      {#if !loading && !error}<p class="empty pad">No user yet: one is created automatically the first time someone signs in.</p>{/if}
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
