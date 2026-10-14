<script lang="ts">
  // Renders a declared form (ADR 0115): sections, labels, help, the issues of each field and the submit button. It
  // knows no process: a page gives it the form, a NodeSource and what to do with the values.
  import { untrack } from 'svelte';
  import type { FormDef, FormValues, NodeSource } from './types';
  import { emptySource } from './types';
  import { widgetOf } from './registry';
  import { registerBuiltinWidgets } from './builtins';
  import { formIssues, initialValues, sections, submittedValues } from './validate';
  import JsonWidget from './widgets/JsonWidget.svelte';

  registerBuiltinWidgets();

  let {
    form,
    source = emptySource,
    busy = false,
    serverError = '',
    onsubmit,
    actions,
  }: {
    form: FormDef;
    source?: NodeSource;
    busy?: boolean;
    /** an error the receiver of the values returned (shown under the button) */
    serverError?: string;
    onsubmit: (values: FormValues) => void;
    /** more buttons next to the submit one */
    actions?: import('svelte').Snippet;
  } = $props();

  const uid = Math.random().toString(36).slice(2, 8);
  // the values start from the defaults of the form it is given (a page gives another form a new renderer)
  let values = $state<FormValues>(untrack(() => initialValues(form)));
  let tried = $state(false);
  const issues = $derived(formIssues(form, values, source));
  const blocked = $derived(Object.keys(issues).length > 0);
  const groups = $derived(sections(form));

  function set(name: string, v: unknown) {
    values = { ...values, [name]: v };
  }

  function submit(e: SubmitEvent) {
    e.preventDefault();
    tried = true;
    if (blocked) return;
    onsubmit(submittedValues(form, values));
  }
</script>

<form class="decl-form" onsubmit={submit} novalidate>
  {#if form.title}<h4 class="title">{form.title}</h4>{/if}
  {#if form.description}<p class="desc">{form.description}</p>{/if}
  {#each groups as g (g.name)}
    <fieldset>
      {#if g.name}<legend>{g.name}</legend>{/if}
      {#each g.fields as f (f.name)}
        {@const w = widgetOf(f.widget)}
        {@const id = `ff-${uid}-${f.name}`}
        {@const shown = tried || values[f.name] !== undefined ? (issues[f.name] ?? []) : []}
        {#if w?.display}
          <div class="display"><w.component {id} field={f} value={undefined} onchange={() => {}} {values} {source} /></div>
        {:else}
          <div class="field" class:invalid={shown.length > 0}>
            <label for={id}>{f.label || f.name}{#if f.required}<span class="req" aria-label="required"> *</span>{/if}</label>
            {#if w}
              <w.component {id} field={f} value={values[f.name]} onchange={(v: unknown) => set(f.name, v)} {values} {source} disabled={busy} invalid={shown.length > 0} />
            {:else}
              <JsonWidget {id} field={f} value={values[f.name]} onchange={(v: unknown) => set(f.name, v)} {values} {source} disabled={busy} invalid={shown.length > 0} />
            {/if}
            {#if f.help}<p class="help">{f.help}</p>{/if}
            {#each shown as msg (msg)}<p class="issue" role="alert">{msg}</p>{/each}
          </div>
        {/if}
      {/each}
    </fieldset>
  {/each}
  <div class="row">
    <button class="primary" type="submit" disabled={busy || (tried && blocked)}>{busy ? 'Sending…' : form.submit || 'Submit'}</button>
    {@render actions?.()}
  </div>
  {#if serverError}<div class="alert" style="margin: 0.6rem 0 0">{serverError}</div>{/if}
</form>

<style>
  .title {
    margin: 0.4rem 0 0.2rem;
  }
  .desc {
    margin: 0 0 0.5rem;
    color: var(--muted);
  }
  fieldset {
    border: none;
    margin: 0 0 0.6rem;
    padding: 0;
  }
  legend {
    font-weight: 600;
    font-size: 0.85rem;
    color: var(--muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
    margin-bottom: 0.3rem;
  }
  .field {
    margin-bottom: 0.7rem;
  }
  .field :global(input:not([type='checkbox'])),
  .field :global(textarea),
  .field :global(select) {
    width: 100%;
  }
  .req {
    color: var(--danger);
  }
  .help {
    color: var(--muted);
    font-size: 0.8rem;
    margin: 0.2rem 0 0;
  }
  .issue {
    color: var(--danger);
    font-size: 0.8rem;
    margin: 0.2rem 0 0;
  }
  .invalid :global(input),
  .invalid :global(textarea),
  .invalid :global(select) {
    border-color: var(--danger);
  }
  .display {
    margin-bottom: 0.6rem;
  }
</style>
