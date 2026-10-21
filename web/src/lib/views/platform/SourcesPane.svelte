<script lang="ts">
  // The sources of the logical URLs (ADR 0154): where the things a URL lnk:<source>/<type>/<key> names live, another
  // graph or a directory of files. Declared once on the platform (platform@Source nodes); each project then says which
  // of them its URLs read (the project tab, "Sources"). Edits are staged like the other settings (a personal change of
  // the platform namespace, saved from the dialog's Save bar).
  import { errorMessage, type GraphNode } from '../../api';
  import { headGraph } from '../../graphEdit';
  import { ns } from '../../stores/session.svelte';
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import { stageRetire, stageUpsert } from '../../stores/pending.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { SOURCE_KINDS, SOURCE_TYPE, overlaySources, sourceKey, sourceProblem, sourceProps, type Source } from '../../sources';

  let applied = $state<GraphNode[]>([]);
  let loaded = $state(false);
  let error = $state('');
  let busy = $state(false);
  let form = $state<Source | undefined>();
  let editing = $state(false); // an existing source: its name is fixed

  const sources = $derived(overlaySources(applied, ns.platform));
  const problem = $derived(form ? sourceProblem(form) : '');

  async function load() {
    try {
      const head = await headGraph(ns.platform);
      applied = head.nodes.filter((n) => n.type === SOURCE_TYPE);
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loaded = true;
    }
  }

  // read again once a save commits the platform namespace
  $effect(() => {
    void stamp(keyOf.namespace(ns.platform));
    void load();
  });

  const empty = (): Source => ({ name: '', description: '', kind: 'graph', namespace: '', location: '', branch: '', params: '' });

  function add() {
    form = empty();
    editing = false;
    error = '';
  }

  function edit(s: Source) {
    form = { ...s };
    editing = true;
    error = '';
  }

  async function run(fn: () => Promise<void>) {
    busy = true;
    error = '';
    try {
      await fn();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  const save = () =>
    form &&
    !problem &&
    run(async () => {
      if (!editing && sources.some((s) => s.name === form!.name)) throw new Error(`A source named ${form!.name} is already declared.`);
      await stageUpsert(ns.platform, SOURCE_TYPE, sourceKey(form!.name), sourceProps(form!));
      form = undefined;
    });

  async function remove(s: Source) {
    if (!(await confirmDialog({ message: `Retire the source ${s.name}? The URLs naming it no longer resolve through it (a retired entry can be restored from the graph).`, danger: true }))) return;
    await run(() => stageRetire(ns.platform, SOURCE_TYPE, sourceKey(s.name)));
  }

  const where = (s: Source) => [s.namespace && `namespace ${s.namespace}`, s.branch && `branch ${s.branch}`].filter(Boolean).join(' · ');
</script>

{#if error}<div class="alert">{error}</div>{/if}

<section class="card">
  <div class="row head">
    <h3 class="grow">Declared sources</h3>
    <button type="button" class="small" onclick={add}>New source</button>
  </div>
  <p class="hint">
    A logical URL <code>lnk:&lt;source&gt;/&lt;type&gt;/&lt;key&gt;</code> names a source. A source of kind <strong>graph</strong> is a namespace of a graph: of this one when it
    has no location, of another graph service at its location otherwise. A source of kind <strong>files</strong> is a root directory. A name no source declares is the
    namespace of that name of this graph, so nothing needs declaring to link inside it. Each project chooses which declared source holds the sources its URLs name
    (project tab, <em>Sources</em>; inherited by its sub-projects).
  </p>
  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if sources.length === 0}
    <p class="empty">No source declared: every URL reads a namespace of this graph.</p>
  {:else}
    <div class="scroll">
      <table>
        <thead><tr><th>Source</th><th>Kind</th><th>Location</th><th></th></tr></thead>
        <tbody>
          {#each sources as s (s.name)}
            <tr class:dirty={s.pending}>
              <td>
                <code>{s.name}</code>
                {#if s.description}<div class="hint">{s.description}</div>{/if}
              </td>
              <td>{s.kind}</td>
              <td>
                {#if s.location}<code>{s.location}</code>{:else}<span class="muted">this graph</span>{/if}
                {#if where(s)}<div class="hint">{where(s)}</div>{/if}
              </td>
              <td class="actions">
                {#if s.pending}<span class="pending" title="Not saved yet">unsaved</span>{/if}
                <button type="button" class="small" disabled={busy} onclick={() => edit(s)}>Edit</button>
                <button type="button" class="small danger" disabled={busy} onclick={() => remove(s)}>Retire</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

{#if form}
  <section class="card">
    <h3>{editing ? `Edit ${form.name}` : 'New source'}</h3>
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <label>Name <input type="text" class="mono" bind:value={form.name} disabled={editing} spellcheck="false" placeholder="acme-alm" /></label>
      <label>Description <input type="text" bind:value={form.description} /></label>
      <div class="row">
        <label>
          Kind
          <input type="text" class="mono" list="source-kinds" bind:value={form.kind} spellcheck="false" />
          <datalist id="source-kinds">{#each SOURCE_KINDS as k (k)}<option value={k}></option>{/each}</datalist>
        </label>
        <label>Namespace <input type="text" class="mono" bind:value={form.namespace} spellcheck="false" placeholder="its name" /></label>
        <label>Branch <input type="text" class="mono" bind:value={form.branch} spellcheck="false" placeholder="main" /></label>
      </div>
      <label>
        Location
        <input type="text" class="mono" bind:value={form.location} spellcheck="false" placeholder={form.kind === 'files' ? '/srv/docs' : 'empty: this graph; else https://graph.example'} />
      </label>
      <label>Parameters <textarea rows="3" class="mono" bind:value={form.params} spellcheck="false" placeholder={'{}'}></textarea></label>
      {#if problem}<p class="hint over">{problem}</p>{/if}
      <div class="row">
        <button type="submit" class="small primary" disabled={busy || !!problem}>{editing ? 'Stage the edit' : 'Stage the source'}</button>
        <button type="button" class="small" onclick={() => (form = undefined)}>Cancel</button>
      </div>
    </form>
  </section>
{/if}

<style>
  .scroll {
    overflow-x: auto;
  }
  table {
    min-width: 36rem;
  }
  th,
  td {
    padding: 0.5rem 0.6rem;
    vertical-align: top;
  }
  .row {
    display: flex;
    align-items: end;
    gap: 0.6rem;
    flex-wrap: wrap;
  }
  .row.head {
    align-items: center;
    margin-bottom: 0.3rem;
  }
  .grow {
    flex: 1;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  tr.dirty {
    background: var(--hover);
  }
  .pending {
    display: inline-block;
    margin-right: 0.4rem;
    padding: 0 0.4rem;
    border: 1px solid var(--warn);
    border-radius: 999px;
    color: var(--warn);
    font-size: 0.75rem;
  }
  .form {
    display: grid;
    gap: 0.6rem;
  }
  .form label {
    display: grid;
    gap: 0.2rem;
    font-weight: 500;
  }
  .over {
    color: var(--danger);
  }
</style>
