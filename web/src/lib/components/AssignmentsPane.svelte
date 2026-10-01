<script lang="ts">
  // Assignments of an org unit/user or a project (ADR 0039): the meeting point of organisation and
  // project, granting the roles a unit or user locally holds on a project. Reused, parametrized by which
  // side is fixed, from the Organisation, Project and User editors: the same shape everywhere, one
  // implementation. `fixedOrg` OR `fixedProject` is given by the host tab, never both.
  import Icon from '../shell/Icon.svelte';
  import { errorMessage, nodeTitle, type Struct } from '../api';
  import { findNode, applyOnMain, createNodeItem, updateNodeItem, deleteNodeItem, refOf, type HeadGraph } from '../graphEdit';
  import { notify } from '../shell/workbench.svelte';
  import { confirmDialog } from '../shell/confirmState.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { ORG_UNIT_TYPE, USER_TYPE, PROJECT_UNIT_TYPE, ASSIGNMENT_TYPE, ASSIGNS_ORG, ASSIGNS_PROJECT } from '../orgTypes';
  import { projectRoles, applicableMethodologies, type ProjectRole } from '../projectRoles';

  const NS = 'organisation';

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
  const orgNodes = $derived(head.nodes.filter((n) => n.namespace === NS && (n.type === ORG_UNIT_TYPE || n.type === USER_TYPE)).sort((a, b) => (a.key ?? '').localeCompare(b.key ?? '')));
  const projectNodes = $derived(head.nodes.filter((n) => n.namespace === NS && n.type === PROJECT_UNIT_TYPE).sort((a, b) => (a.key ?? '').localeCompare(b.key ?? '')));

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
      .filter((n) => n.namespace === NS && n.type === ASSIGNMENT_TYPE)
      .map((n) => ({
        node: n,
        org: targetKey(n, ASSIGNS_ORG),
        project: targetKey(n, ASSIGNS_PROJECT),
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
  // the roles the chosen project needs (ADR 0043): an assignment grants some of them
  const project = $derived(fixedProject ?? fProject);
  let available = $state<ProjectRole[]>([]);
  $effect(() => {
    const p = project;
    if (!p) {
      available = [];
      return;
    }
    let cancelled = false;
    void projectRoles(head, p).then((r) => {
      if (!cancelled) available = r;
    });
    return () => {
      cancelled = true;
    };
  });
  /** roles of the assignment no applicable methodology declares (any more) */
  const stray = $derived(fRoles.filter((r) => !available.some((a) => a.name === r)));

  function toggleRole(name: string, on: boolean) {
    fRoles = on ? [...fRoles, name] : fRoles.filter((r) => r !== name);
  }
  let fDescription = $state('');
  let error = $state('');
  let saving = $state(false);
  let editing: Row | undefined = $state(undefined);

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
    fProject = r.project;
    fRoles = [...r.roles];
    fDescription = r.description;
    error = '';
    adding = true;
  }

  async function save() {
    const org = fixedOrg ?? fOrg;
    const project = fixedProject ?? fProject;
    if (!org || !project) {
      error = 'Pick an organisation unit and a project.';
      return;
    }
    const orgNode = orgNodes.find((n) => n.key === org);
    const projectNode = projectNodes.find((n) => n.key === project);
    if (!orgNode || !projectNode) {
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
      const key = `ASG:${org}/${project}`;
      const description = fDescription.trim();
      const existing = editing?.node ?? findNode(head, NS, ASSIGNMENT_TYPE, key);
      const props: Struct = { roles: roles.length ? roles : null, description: description || null };
      const edit = existing
        ? updateNodeItem(existing, props)
        : createNodeItem(key, ASSIGNMENT_TYPE, { roles, ...(description ? { description } : {}) }, [
            { type: ASSIGNS_ORG, to: refOf(orgNode) },
            { type: ASSIGNS_PROJECT, to: refOf(projectNode) },
          ]);
      await applyOnMain(NS, `Assignment ${org} / ${project}`, `${existing ? 'Update' : 'Create'} the assignment of ${org} on ${project}`, head.baselineId, [edit]);
      notify(`Assignment saved: ${org} on ${project}.`, 'ok');
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
      await applyOnMain(NS, `Remove assignment`, `Remove the assignment of ${r.org} on ${r.project}`, head.baselineId, [deleteNodeItem(r.node)]);
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
              ><button type="button" class="link mono" onclick={() => openTab({ kind: r.org.startsWith('USR:') ? 'user' : 'unit', params: { key: r.org } })}>{label(r.org, orgNodes)}</button
              ></td
            >
          {/if}
          {#if !fixedProject}
            <td><button type="button" class="link mono" onclick={() => openTab({ kind: 'project', params: { key: r.project } })}>{label(r.project, projectNodes)}</button></td>
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
    <div class="grid">
      {#if fixedOrg}
        <div class="field"><label for="asg-org">Organisation</label><input id="asg-org" value={label(fixedOrg, orgNodes)} disabled /></div>
      {:else}
        <div class="field">
          <label for="asg-org">Organisation unit / user</label>
          <select id="asg-org" bind:value={fOrg} disabled={!!editing}>
            <option value="">Choose…</option>
            {#each orgNodes as n (n.id)}<option value={n.key}>{nodeTitle(n) || n.key} {n.type === USER_TYPE ? '(user)' : ''}</option>{/each}
          </select>
        </div>
      {/if}
      {#if fixedProject}
        <div class="field"><label for="asg-proj">Project</label><input id="asg-proj" value={label(fixedProject, projectNodes)} disabled /></div>
      {:else}
        <div class="field">
          <label for="asg-proj">Project</label>
          <select id="asg-proj" bind:value={fProject} disabled={!!editing}>
            <option value="">Choose…</option>
            {#each projectNodes as n (n.id)}<option value={n.key}>{nodeTitle(n) || n.key}</option>{/each}
          </select>
        </div>
      {/if}
      <fieldset class="field roles">
        <legend>Roles</legend>
        {#if !project}
          <span class="muted">Pick a project first.</span>
        {:else if !available.length && !stray.length}
          <span class="muted">{applicableMethodologies(head, project).length ? 'The methodologies of this project declare no role.' : 'This project names no methodology: choose its applicable methodologies on its page first.'}</span>
        {/if}
        {#each available as r (r.name)}
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
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
</style>
