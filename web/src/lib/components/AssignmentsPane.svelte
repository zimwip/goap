<script lang="ts">
  // Assignments of an org unit/user or a project (ADR 0039): the meeting point of organisation and
  // project, granting the roles a unit or user locally holds on a project. Reused, parametrized by which
  // side is fixed, from the Organisation, Project and User editors: the same shape everywhere, one
  // implementation. `fixedOrg` OR `fixedProject` is given by the host tab, never both.
  import { types as nodeTypes, links as linkTypes, ns, platformRoles, assignmentKey, isUserKey } from '../stores/session.svelte';
  import Icon from '../shell/Icon.svelte';
  import { errorMessage, nodeTitle, type Struct } from '../api';
  import { findNode, applyOnMain, createNodeItem, updateNodeItem, retireNodeItem, refOf, type HeadGraph } from '../graphEdit';
  import { notify } from '../shell/workbench.svelte';
  import { confirmDialog } from '../shell/confirmState.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { openPicker } from '../shell/pickerState.svelte';
  import { projectRoles, applicableMethodologies, type ProjectRole } from '../projectRoles';

  // sentinel project selection meaning "platform-wide, no project" (ADR 0046): a platform role (e.g. reader)
  // holds everywhere, independent of a project's methodologies, so no assigns_project link is written.
  const PLATFORM = '__platform__';

  let {
    head,
    fixedOrg,
    fixedProject,
    autoOpen = false,
    onChanged,
  }: {
    head: HeadGraph;
    fixedOrg?: string;
    fixedProject?: string;
    autoOpen?: boolean;
    onChanged: () => void;
  } = $props();

  const nodeById = $derived(new Map(head.nodes.map((n) => [n.id ?? '', n])));
  const orgNodes = $derived(head.nodes.filter((n) => n.namespace === ns.organisation && (n.type === nodeTypes.orgUnit || n.type === nodeTypes.user)).sort((a, b) => (a.key ?? '').localeCompare(b.key ?? '')));
  const projectNodes = $derived(head.nodes.filter((n) => n.namespace === ns.organisation && n.type === nodeTypes.projectUnit).sort((a, b) => (a.key ?? '').localeCompare(b.key ?? '')));

  function targetKey(a: import('../api').GraphNode, linkType: string): string {
    const l = head.links.find((x) => x.type === linkType && x.from?.id === a.id);
    return l?.to?.id ? (nodeById.get(l.to.id)?.key ?? '') : '';
  }

  interface Row {
    node: import('../api').GraphNode;
    org: string;
    project: string;
    roles: string[];
    description: string;
  }

  const rows = $derived<Row[]>(
    head.nodes
      .filter((n) => n.namespace === ns.organisation && n.type === nodeTypes.assignment)
      .map((n) => ({
        node: n,
        org: targetKey(n, linkTypes.assignsOrg),
        project: targetKey(n, linkTypes.assignsProject),
        roles: Array.isArray(n.props?.['roles']) ? (n.props!['roles'] as string[]) : [],
        description: typeof n.props?.['description'] === 'string' ? (n.props!['description'] as string) : '',
      }))
      .filter((r) => (fixedOrg ? r.org === fixedOrg : true) && (fixedProject ? r.project === fixedProject : true))
      .sort((a, b) => (a.org + a.project).localeCompare(b.org + b.project)),
  );

  function label(key: string, list: import('../api').GraphNode[]): string {
    const n = list.find((x) => x.key === key);
    return n ? nodeTitle(n) || key : key;
  }

  let adding = $state(autoOpen);
  let fOrg = $state(fixedOrg ?? '');
  let fProject = $state(fixedProject ?? '');
  let fRoles = $state<string[]>([]);
  // the roles the chosen project needs (ADR 0043): an assignment grants some of them. PLATFORM: no project
  // chosen, or a real project (even a fixed one, ADR 0047) that needs none of its own (no applicable
  // methodology, or none declaring a role) — either way the roles offered fall back to the built-in platform
  // ones (ADR 0046): a methodology-less project is otherwise unreachable through an Assignment.
  const project = $derived(fixedProject ?? fProject);
  const isPlatformChoice = $derived(project === PLATFORM);
  let available = $state<ProjectRole[]>([]);
  let rolesLoaded = $state(false);
  $effect(() => {
    const p = project;
    rolesLoaded = false;
    if (!p || p === PLATFORM) {
      available = [];
      return;
    }
    let cancelled = false;
    void projectRoles(head, p).then((r) => {
      if (!cancelled) {
        available = r;
        rolesLoaded = true;
      }
    });
    return () => {
      cancelled = true;
    };
  });
  /** a real project (fixed or chosen) with no role of its own: the roles offered fall back to the platform's */
  const platformFallback = $derived(!!project && project !== PLATFORM && rolesLoaded && available.length === 0);
  const isPlatform = $derived(isPlatformChoice || platformFallback);
  /** the roles offered by the current selection: the project's, or the platform's when it offers none */
  const offered = $derived(isPlatform ? platformRoles().map((r): ProjectRole => ({ name: r.name, description: r.description ?? '', methodologies: [] })) : available);
  /** roles of the assignment the current selection no longer declares */
  const stray = $derived(fRoles.filter((r) => !offered.some((a) => a.name === r)));

  function toggleRole(name: string, on: boolean) {
    fRoles = on ? [...fRoles, name] : fRoles.filter((r) => r !== name);
  }
  let fDescription = $state('');
  let error = $state('');
  let saving = $state(false);
  let editing: Row | undefined = $state(undefined);
  const org = $derived(fixedOrg ?? fOrg);
  /** the existing row for the current org/project selection, while creating (not editing) one: an org can
      hold several roles on the same project (or platform-wide) — one Assignment node, several roles — so
      picking a combination that already has one continues it instead of silently overwriting it (ADR 0043). */
  const matchingRow = $derived(adding && !editing && org && project ? rows.find((r) => r.org === org && r.project === (isPlatformChoice ? '' : project)) : undefined);
  $effect(() => {
    if (matchingRow) {
      fRoles = [...matchingRow.roles];
      fDescription = matchingRow.description;
    } else if (adding && !editing) {
      fRoles = [];
      fDescription = '';
    }
  });

  /** opens a searchable picker instead of a plain <select>: stays usable as the organisation grows */
  function pickOrg() {
    openPicker({
      title: 'Organisation unit / user',
      placeholder: 'Search units and users…',
      items: orgNodes.map((n) => ({ key: n.key ?? '', label: nodeTitle(n) || n.key || '', hint: n.type === nodeTypes.user ? 'user' : undefined })),
      onchoose: (key) => (fOrg = key),
    });
  }
  function pickProject() {
    openPicker({
      title: 'Project',
      placeholder: 'Search projects…',
      items: [{ key: PLATFORM, label: '— Platform-wide (no project) —' }, ...projectNodes.map((n) => ({ key: n.key ?? '', label: nodeTitle(n) || n.key || '' }))],
      onchoose: (key) => (fProject = key),
    });
  }

  function startCreate() {
    editing = undefined;
    fOrg = fixedOrg ?? '';
    fProject = fixedProject ?? '';
    fRoles = [];
    fDescription = '';
    error = '';
    adding = true;
  }

  function startEdit(r: Row) {
    editing = r;
    fOrg = r.org;
    fProject = r.project || PLATFORM;
    fRoles = [...r.roles];
    fDescription = r.description;
    error = '';
    adding = true;
  }

  async function save() {
    const org = fixedOrg ?? fOrg;
    const project = fixedProject ?? fProject;
    if (!org || !project) {
      error = 'Pick an organisation unit, and a project (or platform-wide).';
      return;
    }
    // platform-wide either way: explicitly chosen, or this real project falls back to a platform role because
    // it needs none of its own (ADR 0047)
    const platform = isPlatform;
    const orgNode = orgNodes.find((n) => n.key === org);
    const projectNode = platform ? undefined : projectNodes.find((n) => n.key === project);
    if (!orgNode || (!platform && !projectNode)) {
      error = 'Unknown organisation unit or project.';
      return;
    }
    const roles = fRoles;
    if (!roles.length) {
      error = 'Pick at least one role.';
      return;
    }
    saving = true;
    error = '';
    try {
      const key = assignmentKey(org, platform ? undefined : project);
      const description = fDescription.trim();
      const existing = editing?.node ?? findNode(head, ns.organisation, nodeTypes.assignment, key);
      const props: Struct = { roles: roles.length ? roles : null, description: description || null };
      const links = platform ? [{ type: linkTypes.assignsOrg, to: refOf(orgNode) }] : [{ type: linkTypes.assignsOrg, to: refOf(orgNode) }, { type: linkTypes.assignsProject, to: refOf(projectNode!) }];
      const edit = existing ? updateNodeItem(existing, props) : createNodeItem(key, nodeTypes.assignment, { roles, ...(description ? { description } : {}) }, links);
      const target = platform ? 'the platform' : project;
      await applyOnMain(ns.organisation, `Assignment ${org} / ${target}`, `${existing ? 'Update' : 'Create'} the assignment of ${org} on ${target}`, head.baselineId, [edit]);
      notify(`Assignment saved: ${org} on ${target}.`, 'ok');
      adding = false;
      onChanged();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }

  async function remove(r: Row) {
    if (!(await confirmDialog({ message: `Remove the assignment of ${r.org} on ${r.project}?`, danger: true }))) return;
    try {
      await applyOnMain(ns.organisation, `Retire assignment`, `Retire the assignment of ${r.org} on ${r.project}`, head.baselineId, [retireNodeItem(r.node)]);
      notify('Assignment removed.', 'ok');
      onChanged();
    } catch (e) {
      error = errorMessage(e);
    }
  }
</script>

<section class="card">
  <div class="head">
    <h3>Assignments{fixedOrg ? ` of ${label(fixedOrg, orgNodes)}` : fixedProject ? ` on ${label(fixedProject, projectNodes)}` : ''}</h3>
    <span class="grow"></span>
    <button type="button" class="small" onclick={startCreate}><Icon name="plus" size={13} /> Assignment</button>
  </div>
  <p class="hint">
    The roles an organisation unit or a user holds on a project, among those the project's applicable methodologies declare: what a person may do on a project (the steps, agents and actions they may run) depends on the roles they hold there, by their own assignments and those of their units.
  </p>
  {#if error && !adding}<div class="alert">{error}</div>{/if}
  <table class="tbl">
    <thead>
      <tr>
        {#if !fixedOrg}<th>Organisation</th>{/if}
        {#if !fixedProject}<th>Project</th>{/if}
        <th>Roles</th>
        <th></th>
      </tr>
    </thead>
    <tbody>
      {#each rows as r (r.node.id)}
        <tr>
          {#if !fixedOrg}
            <td
              ><button type="button" class="link mono" onclick={() => openTab({ kind: isUserKey(r.org) ? 'user' : 'unit', params: { key: r.org } })}>{label(r.org, orgNodes)}</button
              ></td
            >
          {/if}
          {#if !fixedProject}
            <td>
              {#if r.project}
                <button type="button" class="link mono" onclick={() => openTab({ kind: 'project', params: { key: r.project } })}>{label(r.project, projectNodes)}</button>
              {:else}
                <span class="muted">Platform-wide</span>
              {/if}
            </td>
          {/if}
          <td>{r.roles.join(', ') || '—'}</td>
          <td class="acts">
            <button type="button" class="small" onclick={() => startEdit(r)}>Edit</button>
            <button type="button" class="small danger" onclick={() => remove(r)}>Remove</button>
          </td>
        </tr>
      {:else}
        <tr><td colspan={(!fixedOrg && !fixedProject ? 4 : 3)} class="empty">No assignment yet.</td></tr>
      {/each}
    </tbody>
  </table>
</section>

{#if adding}
  <section class="card">
    <h3>{editing ? 'Edit' : 'New'} assignment</h3>
    {#if error}<div class="alert">{error}</div>{/if}
    {#if matchingRow}<div class="hint">An assignment already exists for this selection, holding {matchingRow.roles.join(', ')}: pre-filled below. Check more roles to add them, uncheck to remove.</div>{/if}
    <div class="grid">
      {#if fixedOrg}
        <div class="field"><label for="asg-org">Organisation</label><input id="asg-org" value={label(fixedOrg, orgNodes)} disabled /></div>
      {:else}
        <div class="field">
          <label for="asg-org">Organisation unit / user</label>
          <button type="button" id="asg-org" class="picker-btn" disabled={!!editing} onclick={pickOrg}>{fOrg ? label(fOrg, orgNodes) : 'Choose…'}</button>
        </div>
      {/if}
      {#if fixedProject}
        <div class="field"><label for="asg-proj">Project</label><input id="asg-proj" value={label(fixedProject, projectNodes)} disabled /></div>
      {:else}
        <div class="field">
          <label for="asg-proj">Project</label>
          <button type="button" id="asg-proj" class="picker-btn" disabled={!!editing} onclick={pickProject}>{fProject === PLATFORM ? '— Platform-wide (no project) —' : fProject ? label(fProject, projectNodes) : 'Choose…'}</button>
        </div>
      {/if}
      <fieldset class="field roles">
        <legend>Roles</legend>
        {#if !project}
          <span class="muted">Pick a project, or platform-wide, first.</span>
        {:else if platformFallback}
          <span class="muted"
            >{applicableMethodologies(head, project).length
              ? 'The methodologies of this project declare no role: pick a platform role instead. It holds everywhere, not just on this project.'
              : 'This project names no methodology, so it has no role of its own: pick a platform role instead. It holds everywhere, not just on this project — choose the project’s applicable methodologies on its page if you meant a role scoped to it.'}</span
          >
        {:else if !offered.length && !stray.length && !rolesLoaded}
          <span class="muted">Loading…</span>
        {/if}
        {#each offered as r (r.name)}
          <label class="check" title={`${r.description}${r.description ? ' — ' : ''}${r.methodologies.join(', ')}`}>
            <input type="checkbox" checked={fRoles.includes(r.name)} onchange={(e) => toggleRole(r.name, e.currentTarget.checked)} />
            {r.name}
          </label>
        {/each}
        {#each stray as r (r)}
          <label class="check" title="No applicable methodology of the project declares this role">
            <input type="checkbox" checked onchange={(e) => toggleRole(r, e.currentTarget.checked)} />
            {r} <span class="muted">(not declared)</span>
          </label>
        {/each}
      </fieldset>
      <div class="field">
        <label for="asg-desc">Description</label>
        <input id="asg-desc" bind:value={fDescription} />
      </div>
    </div>
    <div class="row">
      <button type="button" class="small primary" disabled={saving} onclick={save}>Save</button>
      <button type="button" class="small" onclick={() => (adding = false)}>Cancel</button>
    </div>
  </section>
{/if}

<style>
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.3rem;
  }
  .head h3 {
    margin: 0;
  }
  .tbl {
    width: 100%;
    border-collapse: collapse;
  }
  .tbl th,
  .tbl td {
    text-align: left;
    padding: 0.25rem 0.5rem;
    border-bottom: 1px solid var(--border, #8884);
  }
  .acts {
    white-space: nowrap;
    text-align: right;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
  }
  .roles {
    grid-column: 1 / -1;
    flex-direction: row;
    flex-wrap: wrap;
    gap: 0.3rem 1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.4rem 0.6rem;
  }
  .picker-btn {
    width: 100%;
    font: inherit;
    color: inherit;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.25rem 0.45rem;
    min-height: 26px;
    text-align: left;
    cursor: pointer;
  }
  .picker-btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
</style>
