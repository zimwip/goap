<script lang="ts">
  // Renders a declared form (ADR 0115): sections, labels, help, the issues of each field and the submit button. It
  // knows no process: a page gives it the form, a NodeSource and what to do with the values.
  import { untrack } from 'svelte';
  import type { FormDef, FormValues, NodeSource } from './types';
  import { emptySource } from './types';
  import { registerBuiltinWidgets } from './builtins';
  import { formIssues, initialValues, isRequired, sections, submittedValues, visibleState } from './validate';
  import FieldRow from './FieldRow.svelte';

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
  const shown = $derived(visibleState(form.fields, values));
  // the sections with the fields their conditions show
  const groups = $derived(sections(form).map((g) => ({ ...g, fields: g.fields.filter((f) => shown.visible.has(f.name)) })).filter((g) => g.fields.length > 0));

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
        <FieldRow
          id={`ff-${uid}-${f.name}`}
          field={f}
          value={values[f.name]}
          onchange={(v: unknown) => set(f.name, v)}
          values={shown.seen}
          {source}
          disabled={busy}
          required={isRequired(f, shown.seen)}
          issues={tried || values[f.name] !== undefined ? (issues[f.name] ?? []) : []}
        />
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
</style>
