<script lang="ts">
  // "Import from a source" tab (ADR 0148): the importers the domain of a namespace declares (a git repository for the
  // code domain) and the form of the one picked from the parameters it declares. A run is done by the importer service
  // implementing it, through the change operations; a run is a job (ADR 0149), followed from the Jobs of the status bar
  // and the Jobs tab, where a click opens its detail.
  import EditorActions from '../../shell/EditorActions.svelte';
  import { imports, errorMessage, type Importer } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import DirectoryChooser from '../../components/DirectoryChooser.svelte';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { refreshBranches } from '../../stores/commitTool.svelte';
  import { onJobEnded, openJob, takeJob } from '../../stores/jobs.svelte';
  import { initialValues, isImportJob, paramFields, runParams, type ParamField } from '../../importSources';

  let { tab }: { tab: Tab } = $props();

  const namespace = $derived(tab.params.ns ?? '');

  let importers = $state<Importer[]>([]);
  let picked = $state('');
  let values = $state<Record<string, string | boolean>>({});
  let loading = $state(true);
  let starting = $state(false);
  let error = $state('');
  /** the parameter whose folder chooser is open */
  let choosing = $state('');

  const current = $derived(importers.find((im) => `${im.domain}/${im.name}` === picked));
  const fields = $derived<ParamField[]>(current ? paramFields(current) : []);

  async function load() {
    try {
      importers = (await imports.list(namespace)).importers ?? [];
      if (!current && importers.length) pick(`${importers[0].domain}/${importers[0].name}`);
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  function pick(key: string) {
    picked = key;
    const im = importers.find((x) => `${x.domain}/${x.name}` === key);
    values = im ? initialValues(paramFields(im)) : {};
  }

  $effect(() => {
    void namespace;
    loading = true;
    void load();
  });

  // a run that ends changed the branches
  $effect(() => onJobEnded((j) => isImportJob(j, namespace) && refreshBranches()));

  async function start(): Promise<string | undefined> {
    if (!current) return 'Pick a source.';
    const params = runParams(fields, values);
    if (typeof params === 'string') {
      error = params;
      return params;
    }
    starting = true;
    error = '';
    try {
      const { job } = await imports.start({ domain: current.domain ?? '', importer: current.name ?? '', params });
      takeJob(job);
      // its progress is in the Jobs of the status bar; the detail shows it at once
      openJob(job);
      notify(`Import started: ${current.label || current.name}.`, 'ok');
    } catch (e) {
      error = errorMessage(e);
      return error;
    } finally {
      starting = false;
    }
  }

  provideActions(
    () => tab.id,
    () => [
      {
        id: 'save',
        label: starting ? 'Starting…' : 'Start import',
        icon: 'upload',
        primary: true,
        disabled: starting || !current?.available,
        run: () => void start(),
      },
    ],
  );
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="database" size={18} />
    <h2>Import from a source{namespace ? ` into ${namespace}` : ''}</h2>
    <EditorActions tabId={tab.id} />
  </div>
  <section class="card">
    {#if loading}
      <p class="hint">Loading…</p>
    {:else if !importers.length}
      <p class="hint">
        The domain of {namespace || 'this namespace'} declares no source. A domain declares its sources under <code>importers:</code>, each run
        by an importer service that registers with the platform.
      </p>
    {:else}
      <p class="hint">
        A source loads its content as changes of the graph, the way the domain declares it: for a git repository, one change per commit. A
        run resumes where the last one stopped.
      </p>
      <div class="field">
        <label for="src-importer">Source</label>
        <select id="src-importer" value={picked} onchange={(e) => pick(e.currentTarget.value)}>
          {#each importers as im (`${im.domain}/${im.name}`)}
            <option value={`${im.domain}/${im.name}`}>{im.label || im.name}{namespace ? '' : ` (${im.domain})`}{im.available ? '' : ' (unavailable)'}</option>
          {/each}
        </select>
      </div>
      {#if current}
        {#if current.description}<p class="hint">{current.description}</p>{/if}
        {#if !current.available}
          <div class="alert warn">No importer service implements {current.label || current.name} now: deploy one (it registers itself) to run it.</div>
        {/if}
        {#each fields as f (f.name)}
          {#if f.kind === 'boolean'}
            <label class="check field" title={f.description}>
              <input type="checkbox" checked={values[f.name] === true} onchange={(e) => (values[f.name] = e.currentTarget.checked)} />
              {f.label}
              {#if f.description}<span class="opt hint">{f.description}</span>{/if}
            </label>
          {:else}
            <div class="field">
              <label for="src-{f.name}">{f.label}</label>
              {#if f.kind === 'enum'}
                <select id="src-{f.name}" value={values[f.name]} onchange={(e) => (values[f.name] = e.currentTarget.value)}>
                  <option value=""></option>
                  {#each f.options as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
                </select>
              {:else}
                <div class="with-chooser">
                  <input
                    id="src-{f.name}"
                    type={f.kind === 'number' ? 'number' : 'text'}
                    value={values[f.name] as string}
                    placeholder={f.default}
                    oninput={(e) => (values[f.name] = e.currentTarget.value)}
                  />
                  {#if f.directory}
                    <button type="button" disabled={!current.available} onclick={() => (choosing = f.name)} title="Choose a directory the importer reads">
                      <Icon name="folder" size={14} /> Browse…
                    </button>
                  {/if}
                </div>
              {/if}
              {#if f.description}<span class="hint small">{f.description}</span>{/if}
            </div>
          {/if}
        {/each}
      {/if}
      {#if error}<div class="alert">{error}</div>{/if}
      <button class="primary" type="button" onclick={() => void start()} disabled={starting || !current?.available}>
        {starting ? 'Starting…' : 'Start import'}
      </button>
    {/if}
  </section>
  {#if choosing && current}
    <DirectoryChooser
      domain={current.domain ?? ''}
      importer={current.name ?? ''}
      param={choosing}
      start={(values[choosing] as string) ?? ''}
      onpick={(path) => {
        values[choosing] = path;
        choosing = '';
      }}
      onclose={() => (choosing = '')}
    />
  {/if}
</div>

<style>
  .with-chooser {
    display: flex;
    gap: 6px;
  }
  .with-chooser input {
    flex: 1;
    min-width: 0;
  }
</style>
