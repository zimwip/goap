<script lang="ts">
  import { stamp, keyOf } from '../flux/signals.svelte';
  // Creating a change by hand (ADR 0024): what it is for (title, intent), what it acts on (namespace, the branch it
  // lands on, the baseline it starts from) and how it is held (its own branch, a parent change). No methodology is
  // needed: the change is then operated from the IDE alone. A namespace no change landed in has no baseline: its first
  // change starts from the empty state (ADR 0056).
  import { graph, errorMessage, shortId, type Baseline, type Branch } from '../api';
  import { changes, refreshChanges } from '../stores/catalog.svelte';
  import { loadTypes, typeCatalog } from '../stores/types.svelte';
  import { project, refreshProjects } from '../stores/project.svelte';
  import { rootProject } from '../stores/session.svelte';
  import { defaultChangeProject } from '../changeProject';

  let { oncreated, oncancel }: { oncreated: (id: string) => void; oncancel?: () => void } = $props();

  let title = $state('');
  let intent = $state('');
  let namespace = $state('');
  let branch = $state('main');
  let baselineId = $state('');
  let ownBranch = $state(true);
  let parentId = $state('');
  // a change is created in a project (ADR 0091): the active one unless another is picked; a sub-change has its parent's
  let projectKey = $state('');
  let namespaces = $state<string[]>([]);
  let branches = $state<Branch[]>([]);
  let baselines = $state<Baseline[]>([]);
  let busy = $state(false);
  let error = $state('');

  void loadTypes();
  $effect(() => {
    if (!project.options.length && !project.loading) void refreshProjects();
  });
  $effect(() => {
    if (!projectKey) projectKey = defaultChangeProject(project.current, rootProject(), project.options);
  });
  $effect(() => {
    graph
      .listNamespaces()
      .then((r) => (namespaces = r.namespaces ?? []))
      .catch(() => (namespaces = []));
    if (!changes.loaded) void refreshChanges();
  });
  const choices = $derived([...new Set([...namespaces, ...typeCatalog.cat.namespaces()])].sort());
  $effect(() => {
    if (!namespace && choices.length) namespace = choices.includes('alm') ? 'alm' : choices[0];
  });

  async function loadNamespace(ns: string) {
    if (!ns) return;
    try {
      const [bs, br] = await Promise.all([graph.listBaselines(ns), graph.listBranches(ns)]);
      baselines = (bs.baselines ?? []).slice().reverse();
      branches = (br.branches ?? []).filter((b) => b.status === 'open' && !b.name?.startsWith('change-') && !b.name?.startsWith('flow-'));
      if (!branches.some((b) => b.name === 'main')) branches = [{ name: 'main', namespace: ns, status: 'open' }, ...branches];
      await pickBranch(branch && branches.some((b) => b.name === branch) ? branch : 'main');
      error = '';
    } catch (e) {
      error = errorMessage(e);
    }
  }
  $effect(() => {
    void loadNamespace(namespace);
    void stamp(keyOf.baselines); // a baseline created meanwhile is a base for the change
  });

  /** the change starts from the head of the branch it lands on, unless another baseline is picked */
  async function pickBranch(name: string) {
    branch = name;
    try {
      baselineId = (await graph.getBranch(namespace, name)).head?.id ?? baselines[0]?.id ?? '';
    } catch {
      baselineId = baselines[0]?.id ?? '';
    }
  }

  const parents = $derived(changes.items.filter((c) => c.namespace === namespace && (c.status === 'draft' || c.status === 'active') && c.branch?.startsWith('change-')));

  async function create(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const c = (
        await graph.createChange({
          title: title.trim(),
          intent: intent.trim() || title.trim(),
          namespace,
          baselineId: parentId ? undefined : baselineId || undefined,
          branch: parentId ? undefined : branch,
          ownBranch: parentId ? undefined : ownBranch,
          parentId: parentId || undefined,
          projectId: parentId ? undefined : projectKey,
        })
      ).change;
      if (!c?.id) throw new Error('The change could not be created.');
      await refreshChanges();
      oncreated(c.id);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<form class="new-change" onsubmit={create}>
  <label for="nc-title">Title</label>
  <input id="nc-title" type="text" bind:value={title} placeholder="What the change does" data-no-pin />
  <label for="nc-intent">Intent</label>
  <textarea id="nc-intent" rows="2" bind:value={intent} placeholder="Why: the need it answers"></textarea>
  <label for="nc-ns">Namespace</label>
  <select id="nc-ns" bind:value={namespace}>
    {#each choices as n (n)}<option value={n}>{n}</option>{/each}
  </select>
  <label for="nc-parent">Parent change</label>
  <select id="nc-parent" bind:value={parentId} title="A sub-change forks from the branch of its parent and merges back into it">
    <option value="">none</option>
    {#each parents as c (c.id)}<option value={c.id}>{c.title || shortId(c.id)}</option>{/each}
  </select>
  {#if !parentId}
    <label for="nc-project">Project</label>
    <select id="nc-project" bind:value={projectKey} title="The project the change acts in: the nodes it creates belong to it">
      {#each project.options as o (o.key)}<option value={o.key}>{o.label}</option>{/each}
      {#if !project.options.some((o) => o.key === projectKey)}<option value={projectKey}>{projectKey}</option>{/if}
    </select>
    <label for="nc-branch">Lands on</label>
    <select id="nc-branch" value={branch} onchange={(e) => pickBranch(e.currentTarget.value)}>
      {#each branches as b (b.name)}<option value={b.name}>{b.name}</option>{/each}
    </select>
    <label for="nc-base">Starts from</label>
    {#if baselines.length}
      <select id="nc-base" bind:value={baselineId}>
        {#each baselines as b (b.id)}<option value={b.id}>{b.name || shortId(b.id)} ({b.branch || 'main'})</option>{/each}
      </select>
    {:else}
      <div class="start">
        <span class="hint">{namespace} has no baseline yet: the change starts from the empty state.</span>
      </div>
    {/if}
    <label class="check"><input type="checkbox" bind:checked={ownBranch} /> own branch (options, sub-changes, merge when applied)</label>
  {/if}
  {#if error}<div class="alert small">{error}</div>{/if}
  <div class="row">
    <button type="submit" class="primary small" disabled={busy || !title.trim() || !namespace || (!parentId && !projectKey)}>Create the change</button>
    {#if oncancel}<button type="button" class="small" onclick={oncancel}>Cancel</button>{/if}
  </div>
</form>

<style>
  .new-change {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 0.4rem 0.6rem 0.8rem;
    border-bottom: 1px solid var(--border);
  }
  label {
    font-size: 0.8rem;
    color: var(--muted);
    margin-top: 3px;
  }
  .check {
    display: flex;
    gap: 4px;
    align-items: center;
    color: inherit;
  }
  .start {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .row {
    display: flex;
    gap: 6px;
    margin-top: 6px;
  }
  .alert.small {
    font-size: 0.88em;
  }
</style>
