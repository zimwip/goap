<script lang="ts">
  import { stamp, keyOf } from '../flux/signals.svelte';
  // Creating a change by hand (ADR 0024): what it is for (title, intent), how it is performed (its methodology, one of
  // those of the project, ADR 0096), what it acts on (namespace, the branch it lands on, the baseline it starts from)
  // and how it is held (its own branch, a parent change). The methodology gives the namespace and the goal the change
  // starts with; a sub-change keeps its parent's. A namespace no change landed in has no baseline: its first change
  // starts from the empty state (ADR 0056).
  import { graph, errorMessage, shortId, type Baseline, type Branch } from '../api';
  import { changes, refreshChanges, loadMethodology, published } from '../stores/catalog.svelte';
  import { headGraph } from '../graphEdit';
  import { applicableMethodologies } from '../projectRoles';
  import { openTab } from '../shell/tabs.svelte';
  import { loadTypes, typeCatalog } from '../stores/types.svelte';
  import { project, refreshProjects } from '../stores/project.svelte';
  import { rootProject, ns as sessionNs } from '../stores/session.svelte';
  import { defaultChangeProject, methodologyOffer } from '../changeProject';
  import { assistField, registerAssist, type FieldSpec, type ToolImpl } from '../assist/registry.svelte';

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
  let methodology = $state('');
  // the methodologies of the project (its own and its ancestors'); undefined while read
  let applicableNames = $state<string[] | undefined>(undefined);
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
    const key = projectKey;
    if (!key || parentId) return;
    applicableNames = undefined;
    headGraph(sessionNs.organisation)
      .then((h) => {
        if (key === projectKey) applicableNames = applicableMethodologies(h, key);
      })
      .catch((e) => (error = errorMessage(e)));
  });
  const offer = $derived(applicableNames ? methodologyOffer(applicableNames, methodology, projectKey) : undefined);
  $effect(() => {
    if (offer && methodology !== offer.selected) methodology = offer.selected;
  });
  // the methodology gives the namespace its changes act on
  const methodNamespace = $derived(methodology && !parentId ? (published.get(methodology)?.namespace ?? '') : '');
  $effect(() => {
    if (methodology) void loadMethodology(methodology);
  });
  $effect(() => {
    if (methodNamespace) namespace = methodNamespace;
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

  const canCreate = $derived(!busy && !!title.trim() && !!namespace && (!!parentId || (!!projectKey && !!offer && !offer.blocked && !!methodology)));

  /** Creates the change; true when it was (the error is shown otherwise). */
  async function create(): Promise<boolean> {
    busy = true;
    error = '';
    try {
      const c = (
        await graph.createChange({
          title: title.trim(),
          intent: intent.trim() || title.trim(),
          namespace,
          methodology: parentId ? undefined : methodology,
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
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      busy = false;
    }
  }

  // what the assistant sees and may do here (ADR 0092): the fields that fill the form take no confirmation (the person
  // still creates the change), `create` does
  const titleField: FieldSpec = { id: 'title', label: 'Title', type: 'string', tool: 'set_title', get: () => title, set: (v) => (title = String(v)) };
  const intentField: FieldSpec = { id: 'intent', label: 'Intent', type: 'string', tool: 'set_intent', get: () => intent, set: (v) => (intent = String(v)) };
  const projectField: FieldSpec = {
    id: 'project',
    label: 'Project',
    type: 'enum',
    tool: 'set_project',
    get enum() {
      return project.options.map((o) => o.key);
    },
    get: () => projectKey,
    set: (v) => (projectKey = String(v)),
  };
  const methodologyField: FieldSpec = {
    id: 'methodology',
    label: 'Methodology',
    type: 'enum',
    tool: 'set_methodology',
    get enum() {
      return offer?.options ?? [];
    },
    get: () => methodology,
    set: (v) => (methodology = String(v)),
  };
  const tools: ToolImpl[] = [
    { name: 'set_title', targetOf: () => 'field:title', run: (a) => void (title = String(a.title ?? '')) },
    { name: 'set_intent', targetOf: () => 'field:intent', run: (a) => void (intent = String(a.intent ?? '')) },
    {
      name: 'set_project',
      enabled: () => !parentId,
      targetOf: () => 'field:project',
      describe: (base) => {
        const keys = project.options.map((o) => o.key);
        const p = keys.length && keys.length <= 30 ? { type: 'enum' as const, enum: keys, description: 'the project key' } : { type: 'string' as const, description: 'the project key' };
        return { ...base, args: { ...base.args, properties: { project: p } } };
      },
      run: (a) => {
        if (project.options.length && !project.options.some((o) => o.key === a.project)) return `The project ${String(a.project)} is not one of: ${project.options.map((o) => o.key).join(', ')}.`;
        projectKey = String(a.project);
      },
    },
    {
      name: 'set_methodology',
      enabled: () => !parentId && !!offer?.options.length,
      targetOf: () => 'field:methodology',
      describe: (base) => {
        const names = offer?.options ?? [];
        const p = names.length <= 30 ? { type: 'enum' as const, enum: names, description: 'the methodology name' } : { type: 'string' as const, description: 'the methodology name' };
        return { ...base, args: { ...base.args, properties: { methodology: p } } };
      },
      run: (a) => {
        const names = offer?.options ?? [];
        if (!names.includes(String(a.methodology))) return `The methodology ${String(a.methodology)} is not one of: ${names.join(', ')}.`;
        methodology = String(a.methodology);
      },
    },
    {
      name: 'create',
      enabled: () => canCreate,
      run: async () => ((await create()) ? undefined : error || 'The change could not be created.'),
    },
  ];
  $effect(() =>
    registerAssist({
      screen: () => ({
        kind: 'new_change',
        title: 'New change',
        summary: `A form creates a change in namespace ${namespace || 'none'}, project ${projectKey || 'none'}, methodology ${methodology || 'none'}${offer?.blocked ? ` (${offer.blocked})` : ''}${parentId ? ', as a sub-change' : ''}; the title ${title.trim() ? 'is filled' : 'is empty'}, the intent ${intent.trim() ? 'is filled' : 'is empty'}.`,
      }),
      focus: () => ({ dialogKind: 'new change', pendingAction: 'creating a change', errors: error ? [error] : [] }),
      tools,
    }),
  );
</script>

<form class="new-change" onsubmit={(e) => (e.preventDefault(), void create())}>
  <label for="nc-title">Title</label>
  <input id="nc-title" type="text" bind:value={title} placeholder="What the change does" data-no-pin use:assistField={titleField} />
  <label for="nc-intent">Intent</label>
  <textarea id="nc-intent" rows="2" bind:value={intent} placeholder="Why: the need it answers" use:assistField={intentField}></textarea>
  {#if !parentId}
    <label for="nc-method">Methodology</label>
    {#if offer?.blocked}
      <div class="alert small">
        {offer.blocked}
        <button type="button" class="link" onclick={() => openTab({ kind: 'project', params: { key: projectKey } })}>Open the project</button>
      </div>
    {:else}
      <select id="nc-method" bind:value={methodology} use:assistField={methodologyField} title="How the change is performed: one of the methodologies of its project; it gives the namespace and the goal the change starts with" disabled={!offer}>
        {#if !methodology}<option value="">— choose a methodology —</option>{/if}
        {#each offer?.options ?? [] as m (m)}<option value={m}>{m}</option>{/each}
      </select>
    {/if}
  {/if}
  <label for="nc-ns">Namespace</label>
  <select id="nc-ns" bind:value={namespace} disabled={!!methodNamespace} title={methodNamespace ? `The namespace of the methodology ${methodology}` : ''}>
    {#each choices as n (n)}<option value={n}>{n}</option>{/each}
  </select>
  <label for="nc-parent">Parent change</label>
  <select id="nc-parent" bind:value={parentId} title="A sub-change forks from the branch of its parent and merges back into it">
    <option value="">none</option>
    {#each parents as c (c.id)}<option value={c.id}>{c.title || shortId(c.id)}</option>{/each}
  </select>
  {#if !parentId}
    <label for="nc-project">Project</label>
    <select id="nc-project" bind:value={projectKey} use:assistField={projectField} title="The project the change acts in: the nodes it creates belong to it">
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
    <button type="submit" class="primary small" disabled={!canCreate}>Create the change</button>
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
