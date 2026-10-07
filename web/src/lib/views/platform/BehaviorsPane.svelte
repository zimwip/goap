<script lang="ts">
  // Global behaviours of the LLM calls (ADR 0093): instructions the gateway adds to the system text of every call that
  // matches their scope, so a platform-wide style ("answer tersely") needs no change in any prompt. Edits are staged like
  // the catalog's (a personal change of the platform namespace, saved from the dialog's Save bar); the preview shows what
  // the gateway applies now, so it follows a save.
  import { errorMessage, models, type CatalogModel, type LlmBehavior, type ModelAlias, type PreviewBehaviorsResponse } from '../../api';
  import { saveBehavior, setBehaviorEnabled, retireBehavior, type Unsaved } from '../../llmEdit';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { SOURCES, DEFAULT_MAX_INSTRUCTION, DEFAULT_MAX_TOTAL, counter, emptyForm, formOf, scopeSummary, toggled, validateForm, type BehaviorForm } from '../../behaviors';

  let {
    behaviors,
    catalog,
    aliases,
    maxInstruction = DEFAULT_MAX_INSTRUCTION,
    maxTotal = DEFAULT_MAX_TOTAL,
    sources = SOURCES,
    onchange,
  }: {
    behaviors: Unsaved<LlmBehavior>[];
    catalog: CatalogModel[];
    aliases: ModelAlias[];
    maxInstruction?: number;
    maxTotal?: number;
    sources?: string[];
    onchange: () => Promise<void> | void;
  } = $props();

  let form = $state<BehaviorForm | undefined>();
  let editing = $state(false); // an existing behaviour: its name is fixed
  let error = $state('');
  let busy = $state(false);

  const modelId = (m: { provider: string; model: string }) => `${m.provider}/${m.model}`;
  const cnt = $derived(counter(form?.instruction ?? '', maxInstruction));
  const problem = $derived(form ? validateForm(form, maxInstruction) : '');

  const ordered = $derived([...behaviors].sort((a, b) => (a.order ?? 0) - (b.order ?? 0) || a.name.localeCompare(b.name)));

  function add() {
    form = emptyForm();
    editing = false;
    error = '';
  }

  function edit(b: LlmBehavior) {
    form = formOf(b);
    editing = true;
    error = '';
  }

  async function run(fn: () => Promise<void>) {
    busy = true;
    error = '';
    try {
      await fn();
      await onchange();
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
      await saveBehavior({ ...form!, appliesToJson: form!.appliesToJson, kinds: ['complete'] });
      form = undefined;
    });

  const toggle = (b: LlmBehavior, on: boolean) => run(() => setBehaviorEnabled(b.name, on));

  async function remove(b: LlmBehavior) {
    if (!(await confirmDialog({ message: `Retire the behaviour ${b.name}? It is taken out of the configuration (a retired entry can be restored from the graph).`, danger: true }))) return;
    await run(() => retireBehavior(b.name));
  }

  // --- preview ---------------------------------------------------------------------------

  let pAlias = $state('default');
  let pSource = $state('engine');
  let pJson = $state(false);
  let pSystem = $state('You are a helpful assistant.');
  let preview = $state<PreviewBehaviorsResponse | undefined>();
  let previewError = $state('');

  async function runPreview() {
    previewError = '';
    try {
      preview = await models.previewBehaviors({ alias: pAlias, source: pSource, json: pJson, system: pSystem });
    } catch (e) {
      preview = undefined;
      previewError = errorMessage(e);
    }
  }
</script>

{#if error}<div class="alert">{error}</div>{/if}

<section class="card">
  <div class="row head">
    <h3 class="grow">Global behaviours</h3>
    <button type="button" class="small" onclick={add}>New behaviour</button>
  </div>
  <p class="hint">
    A behaviour is an instruction added to the system text of every completion that matches its scope, whoever asks (runs, assistant, helper…). Disabled by
    default; turn one on to apply it to the whole platform. <strong>Cost:</strong> its text is sent with every matching call, so it adds input tokens to each
    (a behaviour that shortens the answers can still save more than it adds). <strong>JSON calls:</strong> calls that require a structured answer (the
    items of a run, the planners, the assistant, the helper) are skipped unless the behaviour is flagged to apply to them; it is then wrapped so that it
    cannot change the format. Embeddings never get a behaviour. At most {maxTotal} bytes are added to one call; the ones that do not fit are dropped and
    shown as such in the call's prompt.
  </p>
  {#if ordered.length === 0}
    <p class="empty">No behaviour yet.</p>
  {:else}
    <div class="scroll">
      <table>
        <thead><tr><th>Behaviour</th><th>Enabled</th><th>Order</th><th>Applies to</th><th></th></tr></thead>
        <tbody>
          {#each ordered as b (b.name)}
            <tr class:off={!b.enabled} class:dirty={b.pending}>
              <td>
                <code>{b.name}</code> <span class="hint">{b.position === 'prepend' ? 'before' : 'after'} the system text</span>
                {#if b.description}<div class="hint">{b.description}</div>{/if}
              </td>
              <td><input type="checkbox" checked={!!b.enabled} disabled={busy} onchange={(e) => toggle(b, e.currentTarget.checked)} aria-label={`Enable ${b.name}`} /></td>
              <td class="num">{b.order ?? 0}</td>
              <td>{scopeSummary(b)}</td>
              <td class="actions">
                {#if b.pending}<span class="pending" title="Not saved yet">unsaved</span>{/if}
                <button type="button" class="small" onclick={() => edit(b)}>Edit</button>
                <button type="button" class="small danger" onclick={() => remove(b)}>Retire</button>
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
    <h3>{editing ? `Edit ${form.name}` : 'New behaviour'}</h3>
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <label>Name <input type="text" class="mono" bind:value={form.name} disabled={editing} spellcheck="false" placeholder="terse" /></label>
      <label>Description <input type="text" bind:value={form.description} /></label>
      <label>
        Instruction
        <textarea rows="5" bind:value={form.instruction} placeholder="Answer tersely: …"></textarea>
        <span class="hint" class:over={cnt.over}>{cnt.used} / {cnt.max} bytes</span>
      </label>
      <div class="row">
        <label>
          Position
          <select bind:value={form.position}>
            <option value="append">after the system text</option>
            <option value="prepend">before the system text</option>
          </select>
        </label>
        <label>Order <input type="number" step="1" bind:value={form.order} class="narrow" /></label>
        <label class="check"><input type="checkbox" bind:checked={form.enabled} /> Enabled</label>
      </div>
      <fieldset>
        <legend>Scope (every selector narrows; leave all empty for every completion)</legend>
        <div class="pick">
          <span class="hint">Aliases</span>
          {#each aliases as a (a.alias)}
            <label class="check"><input type="checkbox" checked={form.aliases.includes(a.alias)} onchange={() => (form!.aliases = toggled(form!.aliases, a.alias))} /> {a.alias}</label>
          {/each}
        </div>
        <div class="pick">
          <span class="hint">Models</span>
          {#each catalog as m (modelId(m))}
            <label class="check"><input type="checkbox" checked={form.models.includes(modelId(m))} onchange={() => (form!.models = toggled(form!.models, modelId(m)))} /> {modelId(m)}</label>
          {/each}
        </div>
        <div class="pick">
          <span class="hint">Callers</span>
          {#each sources as s (s)}
            <label class="check"><input type="checkbox" checked={form.sources.includes(s)} onchange={() => (form!.sources = toggled(form!.sources, s))} /> {s}</label>
          {/each}
        </div>
        <label class="check"><input type="checkbox" bind:checked={form.appliesToJson} /> Also apply to calls that require a JSON answer (risky for the protocols built on JSON: prefer scoping by caller)</label>
      </fieldset>
      {#if problem}<p class="hint over">{problem}</p>{/if}
      <div class="row">
        <button type="submit" class="primary" disabled={!!problem || busy}>Stage</button>
        <button type="button" onclick={() => (form = undefined)}>Cancel</button>
        <span class="hint">Staged edits are applied with the Save bar below.</span>
      </div>
    </form>
  </section>
{/if}

<section class="card">
  <h3>Preview</h3>
  <p class="hint">The system text a call would be sent with, using the behaviours in force now (saved ones). No model is called.</p>
  <div class="row">
    <label>
      Alias
      <select bind:value={pAlias}>
        {#each aliases as a (a.alias)}<option value={a.alias}>{a.alias}</option>{/each}
      </select>
    </label>
    <label>
      Caller
      <select bind:value={pSource}>
        {#each sources as s (s)}<option value={s}>{s}</option>{/each}
      </select>
    </label>
    <label class="check"><input type="checkbox" bind:checked={pJson} /> requires JSON</label>
    <button type="button" onclick={runPreview}>Preview</button>
  </div>
  <label class="block">System text of the call <textarea rows="2" bind:value={pSystem}></textarea></label>
  {#if previewError}<div class="alert">{previewError}</div>{/if}
  {#if preview}
    <p class="hint">
      {#if preview.applied?.length}Applied: {preview.applied.join(', ')} · about {preview.addedTokens ?? 0} tokens added{:else}No behaviour applies to this call.{/if}
      {#if preview.skipped?.length} · dropped by the size cap: {preview.skipped.join(', ')}{/if}
    </p>
    <pre class="text">{preview.system}</pre>
  {/if}
</section>

<style>
  .scroll {
    overflow-x: auto;
  }
  table {
    min-width: 40rem;
  }
  th,
  td {
    padding: 0.5rem 0.6rem;
    vertical-align: top;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    flex-wrap: wrap;
  }
  .row.head {
    margin-bottom: 0.3rem;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  .num {
    text-align: right;
  }
  tr.dirty {
    background: var(--hover);
  }
  tr.off code {
    opacity: 0.55;
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
  .form label,
  .block {
    display: grid;
    gap: 0.2rem;
    font-weight: 500;
  }
  .row label {
    display: grid;
  }
  label.check {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    font-weight: 400;
  }
  .pick {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 0.8rem;
    align-items: center;
    margin-bottom: 0.4rem;
  }
  .pick > span {
    min-width: 4.5rem;
  }
  .narrow {
    width: 6rem;
  }
  textarea {
    width: 100%;
    font-family: inherit;
  }
  .over {
    color: var(--warn);
  }
  .text {
    margin: 0.4rem 0 0;
    padding: 0.5rem 0.6rem;
    max-height: 40vh;
    overflow: auto;
    background: var(--neutral-soft);
    border-radius: var(--radius-sm);
    white-space: pre-wrap;
    word-break: break-word;
  }
</style>
