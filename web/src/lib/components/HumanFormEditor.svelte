<script lang="ts">
  // The form of a human action (ADR 0115), edited as JSON with a live preview, and the code run on its values.
  // A first editor: the drag-and-drop one with patterns comes next.
  import type { ActionForm } from '../methodologyForm';
  import FormRenderer from '../forms/FormRenderer.svelte';
  import type { FormDef } from '../forms/types';
  import CodeEditor from './CodeEditor.svelte';

  let { action = $bindable(), id, p, bad, readonly = false }: { action: ActionForm; id: string; p: string; bad: (path: string) => boolean; readonly?: boolean } =
    $props();

  const EXAMPLE: FormDef = {
    title: 'Link the requirement to what realizes it',
    submit: 'Link',
    fields: [
      { name: 'requirement', widget: 'node', label: 'Requirement', required: true, constraints: { types: ['alm@Requirement'] } },
      {
        name: 'realizedBy',
        widget: 'link',
        label: 'Realized by',
        constraints: { from: 'requirement', linkTypes: ['alm@realized_by'], toTypes: ['alm@Application'], max: 5 },
      },
      { name: 'why', widget: 'textarea', label: 'Why', required: true },
    ],
  };
  const CODE = `// the checked values of the form, by field name
const f = ctx.var("form");
ctx.addArtifact("link_request", { requirement: f.requirement, why: f.why });
`;

  const parsed = $derived.by((): { form?: FormDef; error?: string } => {
    if (!action.form.trim()) return {};
    try {
      const f: unknown = JSON.parse(action.form);
      if (f === null || typeof f !== 'object' || Array.isArray(f)) return { error: 'a JSON object is expected' };
      return { form: f as FormDef };
    } catch (e) {
      return { error: e instanceof Error ? e.message : String(e) };
    }
  });
</script>

<div class="field">
  <label for="{id}-form">Form <span class="opt">(JSON: title, submit, artifact, fields with widget and constraints)</span></label>
  <textarea id="{id}-form" class="mono" rows="10" bind:value={action.form} class:bad={bad(`${p}.form`) || !!parsed.error} data-path="{p}.form" disabled={readonly}
    placeholder={'{ "fields": [ { "name": "requirement", "widget": "node", "constraints": { "types": ["alm@Requirement"] } } ] }'}></textarea>
  <span class="hint">
    Widgets: text, textarea, number, boolean, date, select, multiselect, node (a node found by a search), link (links to
    create), markdown. Constraints limit the choices; the engine checks them again on submission. Without code, the values
    are recorded as an artifact and the links are written.
    {#if parsed.error}<span class="err">{parsed.error}</span>{/if}
  </span>
  {#if !readonly && !action.form.trim()}
    <button type="button" class="small" onclick={() => (action.form = JSON.stringify(EXAMPLE, null, 2))}>Insert an example</button>
  {/if}
</div>
{#if parsed.form}
  <details class="preview">
    <summary>Preview</summary>
    {#key action.form}
      <FormRenderer form={parsed.form} onsubmit={() => {}} />
    {/key}
  </details>
{/if}
{#if parsed.form || action.code.trim()}
  <div class="field">
    <label for="{id}-fcode">Code run on submission <span class="opt">(optional, JavaScript, <code>ctx.var("form")</code>)</span></label>
    <CodeEditor id="{id}-fcode" bind:value={action.code} language="javascript" dsl {readonly} label="Submission code" minHeight="8rem" maxHeight="40vh"
      bad={bad(`${p}.code`)} path="{p}.code" />
    {#if !readonly && !action.code.trim()}
      <button type="button" class="small" onclick={() => ((action.language = 'javascript'), (action.code = CODE))}>Insert a template</button>
    {/if}
  </div>
{/if}

<style>
  .preview {
    margin: 0 0 0.8rem;
    padding: 0.5rem 0.75rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius-sm);
  }
  .err {
    color: var(--danger);
  }
</style>
