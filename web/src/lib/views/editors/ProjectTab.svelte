<script lang="ts">
  // Project tab (ADR 0039): a ProjectUnit node of the organisation namespace, mirroring OrganisationTab.
  // It names the methodologies that apply (inherited by its sub-projects), which identifies the roles the
  // project needs (ADR 0043); its Assignments pane is the meeting point with organisation: which org units or
  // users hold which of those roles here.
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import AssignmentsPane from '../../components/AssignmentsPane.svelte';
  import { errorMessage } from '../../api';
  import { headGraph, findNode, applyOnMain, updateNodeItem, type HeadGraph } from '../../graphEdit';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';
  import { PROJECT_UNIT_TYPE, PROJECT_PART_OF, DEFAULT_PROJECT_PROP, defaultProject } from '../../orgTypes';
  import { hasAnyRole } from '../../stores/session.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { methodologies, refreshMethodologies } from '../../stores/catalog.svelte';
  import { applicableMethodologies, projectRoles, holders, type ProjectRole } from '../../projectRoles';

  let { tab }: { tab: Tab } = $props();

  const NS = 'organisation';
  const key = $derived(tab.params.key ?? '');

  let head = $state<HeadGraph>();
  let loading = $state(false);
  let error = $state('');
  let pane = $state(tab.params.pane === 'assignments' ? 'assignments' : 'overview');

  const project = $derived(head ? findNode(head, NS, PROJECT_UNIT_TYPE, key) : undefined);
  const nodeById = $derived(new Map((head?.nodes ?? []).map((n) => [n.id ?? '', n])));
  const parentKey = $derived.by(() => {
    const l = head?.links.find((x) => x.type === PROJECT_PART_OF && x.from?.id === project?.id && x.from?.id !== x.to?.id);
    return l?.to?.id ? (nodeById.get(l.to.id)?.key ?? '') : '';
  });
  const childKeys = $derived(
    (head?.links ?? [])
      .filter((l) => l.type === PROJECT_PART_OF && l.to?.id === project?.id && l.from?.id !== l.to?.id)
      .map((l) => nodeById.get(l.from?.id ?? '')?.key ?? '')
      .filter(Boolean)
      .sort(),
  );

  // the roles the project needs (its applicable methodologies, ADR 0043) and who holds them here
  const applicable = $derived(head ? applicableMethodologies(head, key) : []);
  const inherited = $derived(applicable.filter((m) => !ownMethodologies.includes(m)));
  const ownMethodologies = $derived(Array.isArray(project?.props?.['methodologies']) ? (project!.props!['methodologies'] as string[]) : []);
  const held = $derived(head ? holders(head, key) : new Map<string, string[]>());
  let roles = $state<ProjectRole[]>([]);
  $effect(() => {
    if (!head) return;
    let cancelled = false;
    void projectRoles(head, key).then((r) => {
      if (!cancelled) roles = r;
    });
    return () => {
      cancelled = true;
    };
  });
  /** the methodology names the registry knows (any version), to choose from */
  const known = $derived([...new Set(methodologies.items.map((m) => m.name ?? '').filter(Boolean))].sort());

  async function load() {
    loading = true;
    try {
      if (!methodologies.items.length) void refreshMethodologies();
      head = await headGraph(NS);
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void key;
    void stamp(keyOf.namespace(NS));
    void load();
  });

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: load }],
  );

  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview' },
    { id: 'assignments', label: 'Assignments' },
  ]);

  // The default project (ADR 0054): the project a change naming none acts in, flagged by an administrator. Making
  // this project the default moves the flag in one change (set here, cleared on every project carrying it).
  const projects = $derived((head?.nodes ?? []).filter((n) => n.type === PROJECT_UNIT_TYPE));
  const defaultKey = $derived(defaultProject(projects));
  const isAdmin = $derived(hasAnyRole('admin'));
  let defaultBusy = $state(false);

  async function makeDefault() {
    if (!project || !head || defaultKey === key) return;
    const ok = await confirmDialog({
      title: 'Default project',
      message: `Changes that name no project will act in ${key} (instead of ${defaultKey}).`,
      confirmLabel: 'Make default project',
    });
    if (!ok) return;
    defaultBusy = true;
    error = '';
    try {
      const flagged = projects.filter((n) => n.id !== project.id && n.props?.[DEFAULT_PROJECT_PROP] === true);
      await applyOnMain(NS, `Default project ${key}`, `Changes that name no project act in ${key}`, head.baselineId, [
        updateNodeItem(project, { [DEFAULT_PROJECT_PROP]: true }),
        ...flagged.map((n) => updateNodeItem(n, { [DEFAULT_PROJECT_PROP]: null })),
      ]);
      notify(`Changes that name no project now act in ${key}.`, 'ok');
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      defaultBusy = false;
    }
  }

  // ---- overview edit form ---------------------------------------------------------

  let editing = $state(false);
  let fDescription = $state('');
  let fStatus = $state('active');
  let fMethodologies = $state<string[]>([]);
  let saving = $state(false);

  function startEdit() {
    fDescription = typeof project?.props?.['description'] === 'string' ? (project!.props!['description'] as string) : '';
    fStatus = typeof project?.props?.['status'] === 'string' ? (project!.props!['status'] as string) : 'active';
    fMethodologies = [...ownMethodologies];
    editing = true;
  }

  function toggleMethodology(name: string, on: boolean) {
    fMethodologies = on ? [...fMethodologies, name] : fMethodologies.filter((m) => m !== name);
  }

  async function save() {
    if (!project || !head) return;
    saving = true;
    error = '';
    try {
      const methodologies = fMethodologies;
      await applyOnMain(NS, `Project ${key}`, `Update project ${key}`, head.baselineId, [
        updateNodeItem(project, { description: fDescription.trim() || null, status: fStatus.trim() || null, methodologies: methodologies.length ? methodologies : null }),
      ]);
      notify(`Project ${key} updated.`, 'ok');
      editing = false;
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="editor-page">
  {#if error}<div class="alert">{error}</div>{/if}
  {#if !project && !loading}
    <p class="empty">Project {key} is not on the graph.</p>
  {:else if project}
    <EditorPanes {panes} bind:active={pane} label="Project sections">
      {#snippet children(active)}
        {#if active === 'overview'}
          <section class="card">
            <div class="head"><Icon name="diff" size={18} /><h2>{String(project.props?.['name'] ?? key)}</h2></div>
            {#if !editing}
              <dl class="kv">
                <dt>Key</dt><dd><code>{key}</code></dd>
                {#if project.props?.['kind']}<dt>Kind</dt><dd>{project.props['kind']}</dd>{/if}
                {#if project.props?.['status']}<dt>Status</dt><dd>{project.props['status']}</dd>{/if}
                {#if project.props?.['description']}<dt>Description</dt><dd>{project.props['description']}</dd>{/if}
                <dt>Part of</dt>
                <dd>
                  {#if parentKey}
                    <button type="button" class="link mono" onclick={() => openTab({ kind: 'project', params: { key: parentKey } })}>{parentKey}</button>
                  {:else}<span class="muted">none (root)</span>{/if}
                </dd>
                <dt>Default project</dt>
                <dd>
                  {#if defaultKey === key}
                    <span class="badge">default: changes that name no project act in it</span>
                  {:else}
                    <span class="muted">the default is <button type="button" class="link mono" onclick={() => openTab({ kind: 'project', params: { key: defaultKey } })}>{defaultKey}</button></span>
                    {#if isAdmin}<button type="button" class="small ghost" disabled={defaultBusy} onclick={makeDefault}>Make default project</button>{/if}
                  {/if}
                </dd>
                {#if childKeys.length}
                  <dt>Sub-projects</dt>
                  <dd>
                    {#each childKeys as c (c)}<button type="button" class="link mono" onclick={() => openTab({ kind: 'project', params: { key: c } })}>{c}</button>{' '}{/each}
                  </dd>
                {/if}
                <dt>Applicable methodologies</dt>
                <dd>
                  {#if ownMethodologies.length}<code>{ownMethodologies.join(', ')}</code>{:else}<span class="muted">none of its own</span>{/if}
                  {#if inherited.length}<span class="muted"> · inherited: <code>{inherited.join(', ')}</code></span>{/if}
                </dd>
              </dl>
              <p class="hint">A project names the methodologies that apply to it (its sub-projects inherit them): the roles they declare are the ones the project needs, and an Assignment grants some of them to an org unit or a user here. From one project to another, the same person can hold different roles, and so do different things.</p>
              <button type="button" class="small" onclick={startEdit}>Edit</button>
            {:else}
              <div class="grid">
                <div class="field">
                  <label for="proj-status">Status</label>
                  <input id="proj-status" bind:value={fStatus} />
                </div>
                <div class="field">
                  <label for="proj-desc">Description</label>
                  <input id="proj-desc" bind:value={fDescription} />
                </div>
              </div>
              <fieldset class="meths">
                <legend>Applicable methodologies</legend>
                {#each [...new Set([...known, ...fMethodologies])] as name (name)}
                  <label class="check">
                    <input type="checkbox" checked={fMethodologies.includes(name)} onchange={(e) => toggleMethodology(name, e.currentTarget.checked)} />
                    {name}{#if !known.includes(name)}<span class="muted"> (not in the registry)</span>{/if}{#if inherited.includes(name)}<span class="muted"> (inherited)</span>{/if}
                  </label>
                {:else}
                  <span class="muted">No methodology in the registry.</span>
                {/each}
              </fieldset>
              <div class="row">
                <button type="button" class="small primary" disabled={saving} onclick={save}>Save</button>
                <button type="button" class="small" onclick={() => (editing = false)}>Cancel</button>
              </div>
            {/if}
          </section>
          {#if !editing}
            <section class="card">
              <h3>Roles needed on this project</h3>
              {#if !applicable.length}
                <p class="muted">The project names no methodology: it needs no role yet. Edit it to choose the methodologies that apply.</p>
              {:else}
                <table class="tbl">
                  <thead><tr><th>Role</th><th>Declared by</th><th>Held by</th></tr></thead>
                  <tbody>
                    {#each roles as r (r.name)}
                      <tr>
                        <td><code>{r.name}</code>{#if r.description}<div class="muted small-text">{r.description}</div>{/if}</td>
                        <td>{r.methodologies.join(', ')}</td>
                        <td>
                          {#each held.get(r.name) ?? [] as who (who)}
                            <button type="button" class="link mono" onclick={() => openTab({ kind: who.startsWith('USR:') ? 'user' : 'unit', params: { key: who } })}>{who}</button>{' '}
                          {:else}<span class="tag warn">unassigned</span>{/each}
                        </td>
                      </tr>
                    {:else}
                      <tr><td colspan="3" class="muted">The applicable methodologies declare no role.</td></tr>
                    {/each}
                  </tbody>
                </table>
                <button type="button" class="small" onclick={() => (pane = 'assignments')}>Assign roles…</button>
              {/if}
            </section>
          {/if}
        {:else if head}
          <AssignmentsPane {head} fixedProject={key} autoOpen={tab.params.newAssignment === '1'} onChanged={load} />
        {/if}
      {/snippet}
    </EditorPanes>
  {/if}
</div>

<style>
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.6rem;
  }
  .head h2 {
    margin: 0;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.25rem 1rem;
  }
  .kv dt {
    color: var(--muted);
  }
  .kv dd {
    margin: 0;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
  }
  .meths {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 1rem;
    margin: 0.6rem 0;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.4rem 0.6rem;
  }
  .tbl {
    width: 100%;
    border-collapse: collapse;
    margin-bottom: 0.5rem;
  }
  .tbl th,
  .tbl td {
    text-align: left;
    padding: 0.25rem 0.5rem;
    border-bottom: 1px solid var(--border, #8884);
    vertical-align: top;
  }
  .small-text {
    font-size: 0.85em;
  }
  .tag.warn {
    color: var(--warn);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
</style>
