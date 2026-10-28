<script lang="ts">
  // "Import" tab: import a methodology or a domain (YAML or JSON), or a graph snapshot (JSON, ADR 0104), from a file or
  // a text. The tab's `what` param says which; a methodology by default.
  import EditorActions from '../../shell/EditorActions.svelte';
  import { registry, graph, errorMessage, type Issue, type GraphImportResult } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import CodeEditor from '../../components/CodeEditor.svelte';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { refreshMethodologies } from '../../stores/catalog.svelte';
  import { refreshDomains } from '../../stores/domains.svelte';
  import { refreshBranches } from '../../stores/commitTool.svelte';
  import { methodologySpec } from './methodologyTabs';
  import { domainSpec } from './domainTabs';
  import { importKind, IMPORT_KINDS } from './importKinds';
  import { registerAssist } from '../../assist/registry.svelte';
  import { followJob, knownJobs, openJob } from '../../stores/jobs.svelte';
  import { graphImportResult } from '../../jobView';
  import { contentFacts, importSummary } from '../../assist/importScreen';

  let { tab }: { tab: Tab } = $props();

  const what = $derived(importKind(tab.params.what));
  const kind = $derived(IMPORT_KINDS[what]);

  let text = $state('');
  let publish = $state(false);
  let title = $state('');
  let importing = $state(false);
  let error = $state('');
  /** a methodology or domain imported: its name, version, status and issues */
  let definition = $state<{ name: string; version: string; status?: string; issues: Issue[] } | null>(null);
  /** the job of a graph import (ADR 0149), live from the job stream (its progress in its detail and the Jobs); its result
   *  once it is done */
  let graphJobId = $state('');
  const graphJob = $derived(graphJobId ? knownJobs.get(graphJobId) : undefined);
  const graphResult = $derived<GraphImportResult | null>(graphJob?.status === 'done' ? graphImportResult(graphJob) : null);

  async function readFile(e: Event & { currentTarget: HTMLInputElement }) {
    const file = e.currentTarget.files?.[0];
    if (!file) return;
    try {
      text = await file.text();
    } catch (err) {
      error = errorMessage(err);
    }
  }

  async function doImport(): Promise<string | undefined> {
    if (!text.trim()) return 'No content is loaded: pick a file or paste it.';
    importing = true;
    error = '';
    definition = null;
    graphJobId = '';
    try {
      if (what === 'graph') {
        const { job } = await graph.startGraphImport({ document: text, ...(title.trim() ? { title: title.trim() } : {}) });
        graphJobId = job.id ?? '';
        openJob(job);
        followJob(job, (j) => {
          if (j.status !== 'done') return;
          const r = graphImportResult(j);
          if (r.change) notify(`Change created: ${r.change.title || r.change.id}.`, 'ok');
          else notify('The graph already holds this snapshot: nothing to import.', 'ok');
          refreshBranches();
        });
      } else if (what === 'domain') {
        const res = await registry.importDomain(text, publish);
        const d = res.domain;
        definition = { name: d?.name ?? '', version: d?.version ?? '', status: d?.status, issues: res.issues ?? [] };
        void refreshDomains();
        if (d) notify(`Imported: ${d.name} v${d.version}.`, 'ok');
      } else {
        const res = await registry.importMethodology(text, publish);
        const m = res.methodology;
        definition = { name: m?.name ?? '', version: m?.version ?? '', status: m?.status, issues: res.issues ?? [] };
        void refreshMethodologies();
        if (m) notify(`Imported: ${m.name} v${m.version}.`, 'ok');
      }
    } catch (err) {
      error = errorMessage(err);
      return error;
    } finally {
      importing = false;
    }
  }

  function openDefinition(name: string, version: string) {
    openTab(what === 'domain' ? domainSpec(name, version) : methodologySpec(name, version), { pin: true });
  }

  provideActions(
    () => tab.id,
    () => [
      {
        id: 'save',
        label: importing ? 'Import…' : 'Import',
        icon: 'upload',
        primary: true,
        shortcut: 'Ctrl+S',
        disabled: importing || !text.trim(),
        run: () => void doImport(),
      },
    ],
  );

  // what the assistant sees and may do here (ADR 0092): a description of the content, never the content itself
  $effect(() =>
    registerAssist({
      tab: tab.id,
      screen: () => ({
        kind: 'import',
        title: kind.heading,
        summary: importSummary(what, text, { publish, title: title.trim() }, {
          definition: definition ? { name: definition.name, version: definition.version, status: definition.status, issues: definition.issues.length } : undefined,
          graph: graphResult
            ? { changeTitle: graphResult.change ? graphResult.change.title || graphResult.change.id : undefined, created: graphResult.created, updated: graphResult.updated, unchanged: graphResult.unchanged, warnings: graphResult.warnings?.length ?? 0 }
            : undefined,
        }),
        entities: graphResult?.change?.id ? [{ type: 'change', id: graphResult.change.id, label: graphResult.change.title || graphResult.change.id }] : [],
      }),
      focus: () => ({ pendingAction: importing || (graphJob && !graphResult && graphJob.status !== 'failed' && graphJob.status !== 'cancelled') ? `importing ${contentFacts(what, text)}` : undefined, errors: error ? [error] : [] }),
      tools: [
        {
          name: 'run_import',
          enabled: () => !importing && !!text.trim(),
          describe: (base) => {
            const props = { ...base.args?.properties };
            if (what === 'graph') delete props.publish;
            else delete props.title;
            return { ...base, args: { properties: props } };
          },
          run: (a) => {
            if (what === 'graph' && typeof a.title === 'string' && a.title.trim()) title = a.title.trim();
            if (what !== 'graph' && typeof a.publish === 'boolean') publish = a.publish;
            return doImport();
          },
        },
      ],
    }),
  );
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="upload" size={18} />
    <h2>{kind.heading}</h2>
    <EditorActions tabId={tab.id} />
  </div>
  <section class="card">
    {#if what === 'graph'}
      <p class="hint">
        The snapshot is the state of one namespace, without history. Importing it opens a change on main that creates the nodes the graph
        lacks and brings the others to the snapshot; nodes the snapshot does not name are left alone. Review, commit and merge the change
        as usual.
      </p>
    {/if}
    <div class="field">
      <label for="imp-file">File</label>
      <input id="imp-file" type="file" accept={kind.accept} onchange={readFile} />
    </div>
    <div class="field">
      <label for="imp-text">… or content</label>
      <CodeEditor id="imp-text" bind:value={text} label="Content" minHeight="18rem" maxHeight="55vh" placeholder={kind.placeholder} />
    </div>
    {#if what === 'graph'}
      <div class="field">
        <label for="imp-title">Title of the change <span class="opt hint">(optional)</span></label>
        <input id="imp-title" type="text" bind:value={title} placeholder="Import of a snapshot of …" />
      </div>
    {:else}
      <label class="check field">
        <input type="checkbox" bind:checked={publish} />
        Publish directly <span class="opt hint">(the definition must be valid)</span>
      </label>
    {/if}
    {#if error}<div class="alert">{error}</div>{/if}
    {#if definition}
      {@const d = definition}
      <div class="alert" class:ok={d.issues.length === 0} class:warn={d.issues.length > 0}>
        {#if d.name}
          Imported: <strong>{d.name}</strong> v{d.version} ({d.status === 'published' ? 'published' : 'draft'}).
          <button type="button" class="link" onclick={() => openDefinition(d.name, d.version)}>Open →</button>
        {:else}
          Import complete.
        {/if}
      </div>
      {#if d.issues.length}
        <ul class="issues">
          {#each d.issues as i, k (k)}
            <li>{#if i.path}<code>{i.path}</code> {/if}{i.message}</li>
          {/each}
        </ul>
      {/if}
    {/if}
    {#if graphJob && !graphResult && graphJob.status !== 'failed' && graphJob.status !== 'cancelled'}
      {@const j = graphJob}
      <p class="hint">Importing the snapshot: it goes on under Jobs. <button type="button" class="link" onclick={() => openJob(j)}>Details</button></p>
    {/if}
    {#if graphResult}
      {@const r = graphResult}
      <div class="alert" class:ok={!r.warnings?.length} class:warn={!!r.warnings?.length}>
        {#if r.change}
          Change <strong>{r.change.title}</strong> created: {r.created ?? 0} node(s) created, {r.updated ?? 0} modified, {r.unchanged ?? 0} unchanged.
          <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: r.change?.id ?? '' } }, { pin: true })}>Open →</button>
        {:else}
          The graph already holds this snapshot ({r.unchanged ?? 0} node(s)): no change created.
        {/if}
      </div>
      {#if r.warnings?.length}
        <ul class="issues">
          {#each r.warnings as w, k (k)}
            <li>{w}</li>
          {/each}
        </ul>
      {/if}
    {/if}
    <button class="primary" type="button" onclick={() => void doImport()} disabled={importing || !text.trim()}>
      {importing ? 'Import…' : 'Import'}
    </button>
  </section>
</div>

<style>
  .issues {
    margin: -0.3rem 0 0.8rem;
    padding-left: 1.2rem;
  }
  .issues code {
    color: var(--danger);
  }
</style>
