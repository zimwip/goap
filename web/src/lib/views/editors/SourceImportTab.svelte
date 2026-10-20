<script lang="ts">
  // "Import from a source" tab (ADR 0148): the importers the domain of a namespace declares (a git repository for the
  // code domain), the form of the one picked from the parameters it declares, and the runs. A run is done by the importer
  // service implementing it, through the change operations; the tab follows the runs while one is going (the importer
  // services publish no event).
  import { imports, errorMessage, type Importer, type ImportJob } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { refreshBranches } from '../../stores/baselineTool.svelte';
  import { initialValues, jobSubject, paramFields, runParams, running, statsLine, type ParamField } from '../../importSources';

  let { tab }: { tab: Tab } = $props();

  const namespace = $derived(tab.params.ns ?? '');

  let importers = $state<Importer[]>([]);
  let jobs = $state<ImportJob[]>([]);
  let picked = $state('');
  let values = $state<Record<string, string | boolean>>({});
  let loading = $state(true);
  let starting = $state(false);
  let error = $state('');

  const current = $derived(importers.find((im) => `${im.domain}/${im.name}` === picked));
  const fields = $derived<ParamField[]>(current ? paramFields(current) : []);

  async function load() {
    try {
      const [ims, js] = await Promise.all([imports.list(namespace), imports.jobs(namespace)]);
      importers = ims.importers ?? [];
      jobs = js.jobs ?? [];
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

  // follow the runs while one is going
  $effect(() => {
    if (!jobs.some(running)) return;
    const t = setInterval(async () => {
      const before = jobs.filter(running).length;
      try {
        jobs = (await imports.jobs(namespace)).jobs ?? [];
      } catch {
        return;
      }
      if (jobs.filter(running).length < before) refreshBranches();
    }, 2000);
    return () => clearInterval(t);
  });

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
      jobs = [job, ...jobs.filter((j) => j.id !== job.id)];
      notify(`Import started: ${current.label || current.name}.`, 'ok');
    } catch (e) {
      error = errorMessage(e);
      return error;
    } finally {
      starting = false;
    }
  }

  async function cancel(j: ImportJob) {
    try {
      const { job } = await imports.cancel(j.service ?? '', j.id ?? '');
      jobs = jobs.map((x) => (x.id === job.id ? job : x));
    } catch (e) {
      error = errorMessage(e);
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
                <input
                  id="src-{f.name}"
                  type={f.kind === 'number' ? 'number' : 'text'}
                  value={values[f.name] as string}
                  placeholder={f.default}
                  oninput={(e) => (values[f.name] = e.currentTarget.value)}
                />
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
  {#if jobs.length}
    <section class="card">
      <h3>Runs</h3>
      <table class="runs">
        <tbody>
          {#each jobs as j (j.id)}
            <tr>
              <td><StatusBadge status={j.status ?? ''} /></td>
              <td>
                <div><strong>{j.importer}</strong> {jobSubject(j)}</div>
                {#if j.progress}<div class="hint small">{j.progress}</div>{/if}
                {#if statsLine(j)}<div class="hint small">{statsLine(j)}</div>{/if}
                {#if j.error}<div class="alert small">{j.error}</div>{/if}
              </td>
              <td class="hint small">{j.by}<br />{j.startedAt ? new Date(j.startedAt).toLocaleString() : ''}</td>
              <td>
                {#if running(j)}
                  <button type="button" class="ghost small" title="Stop this run (it resumes at the next one)" onclick={() => void cancel(j)}
                    ><Icon name="stop" size={14} /></button
                  >
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}
</div>

<style>
  .runs {
    width: 100%;
    border-collapse: collapse;
  }
  .runs td {
    padding: 0.35rem 0.4rem;
    vertical-align: top;
    border-top: 1px solid var(--border);
  }
</style>
