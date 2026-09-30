<script lang="ts">
  // Project tab (ADR 0039): a ProjectUnit node of the organisation namespace, mirroring OrganisationTab.
  // Its Assignments pane is the meeting point with organisation (ADR 0039): which org units or users hold
  // which roles here.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import AssignmentsPane from '../../components/AssignmentsPane.svelte';
  import { errorMessage } from '../../api';
  import { headGraph, findNode, applyOnMain, updateNodeItem, type HeadGraph } from '../../graphEdit';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';
  import { PROJECT_UNIT_TYPE, PROJECT_PART_OF } from '../../orgTypes';

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

  async function load() {
    loading = true;
    try {
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

  // ---- overview edit form ---------------------------------------------------------

  let editing = $state(false);
  let fDescription = $state('');
  let fStatus = $state('active');
  let fMethodologies = $state('');
  let saving = $state(false);

  function startEdit() {
    fDescription = typeof project?.props?.['description'] === 'string' ? (project!.props!['description'] as string) : '';
    fStatus = typeof project?.props?.['status'] === 'string' ? (project!.props!['status'] as string) : 'active';
    fMethodologies = Array.isArray(project?.props?.['methodologies']) ? (project!.props!['methodologies'] as string[]).join(', ') : '';
    editing = true;
  }

  async function save() {
    if (!project || !head) return;
    saving = true;
    error = '';
    try {
      const methodologies = fMethodologies
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
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
                {#if childKeys.length}
                  <dt>Sub-projects</dt>
                  <dd>
                    {#each childKeys as c (c)}<button type="button" class="link mono" onclick={() => openTab({ kind: 'project', params: { key: c } })}>{c}</button>{' '}{/each}
                  </dd>
                {/if}
                <dt>Applicable methodologies</dt>
                <dd>
                  {#if Array.isArray(project.props?.['methodologies']) && (project.props['methodologies'] as string[]).length}
                    <code>{(project.props['methodologies'] as string[]).join(', ')}</code>
                  {:else}<span class="muted">none</span>{/if}
                </dd>
              </dl>
              <p class="hint">A project identifies the methodologies that apply to it, so it inherits the roles they declare (ADR 0035 §2): an Assignment grants a subset of them to an org unit or user.</p>
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
                <div class="field">
                  <label for="proj-meth">Applicable methodologies</label>
                  <input id="proj-meth" placeholder="sdlc, impact-analysis…" bind:value={fMethodologies} />
                </div>
              </div>
              <div class="row">
                <button type="button" class="small primary" disabled={saving} onclick={save}>Save</button>
                <button type="button" class="small" onclick={() => (editing = false)}>Cancel</button>
              </div>
            {/if}
          </section>
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
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
</style>
