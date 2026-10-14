<script lang="ts">
  // The form of a human action (ADR 0115), built with the form designer, and the code run on its values.
  import type { ActionForm } from '../methodologyForm';
  import FormDesigner from '../forms/designer/FormDesigner.svelte';
  import CodeEditor from './CodeEditor.svelte';

  let { action = $bindable(), id, p, bad, readonly = false }: { action: ActionForm; id: string; p: string; bad: (path: string) => boolean; readonly?: boolean } =
    $props();

  const CODE = `// the checked values of the form, by field name
const f = ctx.var("form");
ctx.addArtifact("form_values", f);
`;
</script>

<div class="field" class:bad={bad(`${p}.form`)} data-path="{p}.form">
  <span class="label">Form <span class="opt">(what the person fills in; constraints limit the choices, the engine checks them again)</span></span>
  <FormDesigner bind:value={action.form} disabled={readonly} id="{id}-form" />
</div>
{#if action.form.trim() || action.code.trim()}
  <div class="field">
    <label for="{id}-fcode">Code run on submission <span class="opt">(optional, JavaScript, <code>ctx.var("form")</code>; without code the values are an artifact and the links are written)</span></label>
    <CodeEditor id="{id}-fcode" bind:value={action.code} language="javascript" dsl {readonly} label="Submission code" minHeight="8rem" maxHeight="40vh"
      bad={bad(`${p}.code`)} path="{p}.code" />
    {#if !readonly && !action.code.trim()}
      <button type="button" class="small" onclick={() => ((action.language = 'javascript'), (action.code = CODE))}>Insert a template</button>
    {/if}
  </div>
{/if}

<style>
  .label {
    display: block;
    font-weight: 600;
    margin-bottom: 0.3rem;
  }
  .opt {
    color: var(--muted);
    font-weight: 400;
    font-size: 0.8rem;
  }
  .bad :global(.designer) {
    border-color: var(--danger);
  }
</style>
