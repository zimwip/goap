<script lang="ts">
  // The form designer (ADR 0115): drag widgets or patterns from the palette onto the canvas, reorder the fields by
  // dragging them, edit the selected one on the right; the preview renders the form as a person sees it, the JSON tab
  // is the form as the methodology stores it. The value is that JSON ('' : no form).
  import { untrack } from 'svelte';
  import type { FieldDef, FormDef } from '../types';
  import {
    PATTERNS,
    applyPattern,
    designIssues,
    formJSON,
    insertField,
    labelOf,
    moveField,
    newField,
    palette,
    parseForm,
    removeField,
    updateField,
    type Pattern,
  } from '../design';
  import FormRenderer from '../FormRenderer.svelte';
  import FieldProperties from './FieldProperties.svelte';
  import { loadTypes } from '../../stores/types.svelte';

  let { value = $bindable(''), disabled = false, id = 'form-designer' }: { value?: string; disabled?: boolean; id?: string } = $props();

  void loadTypes();

  const WIDGET = 'text/x-goap-widget';
  const PATTERN = 'text/x-goap-pattern';
  const FIELD = 'text/x-goap-field';

  let view = $state<'design' | 'preview' | 'json'>(untrack(() => (parseForm(value).error ? 'json' : 'design')));
  let selected = $state<number | undefined>(undefined);
  let dropAt = $state<number | undefined>(undefined);
  let jsonDraft = $state(untrack(() => value));
  const parsed = $derived(parseForm(value));
  const form = $derived<FormDef>(parsed.form ?? { fields: [] });
  const fields = $derived(form.fields ?? []);
  const issues = $derived(designIssues(form));
  const count = $derived(Object.values(issues).reduce((n, l) => n + l.length, 0));
  const current = $derived(selected !== undefined ? fields[selected] : undefined);

  function commit(next: FormDef) {
    value = formJSON(next);
    jsonDraft = value;
  }
  const setForm = (patch: Partial<FormDef>) => commit({ ...form, ...patch });

  function addWidget(widget: string, at = fields.length) {
    commit(insertField(form, newField(form, widget), at));
    selected = Math.min(at, fields.length);
  }
  function addPattern(p: Pattern, at = fields.length) {
    commit(applyPattern(form, p, at));
    selected = at;
  }
  function remove(i: number) {
    commit(removeField(form, i));
    selected = undefined;
  }
  function move(from: number, to: number) {
    commit(moveField(form, from, to));
    selected = to > from ? to - 1 : to;
  }
  function changeField(f: FieldDef) {
    if (selected !== undefined) commit(updateField(form, selected, f));
  }

  function over(e: DragEvent, i: number) {
    if (disabled) return;
    e.preventDefault();
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    dropAt = e.clientY < r.top + r.height / 2 ? i : i + 1;
  }
  function drop(e: DragEvent) {
    e.preventDefault();
    const at = dropAt ?? fields.length;
    dropAt = undefined;
    const dt = e.dataTransfer;
    if (!dt || disabled) return;
    const w = dt.getData(WIDGET);
    const p = dt.getData(PATTERN);
    const f = dt.getData(FIELD);
    if (w) addWidget(w, at);
    else if (p) {
      const pat = PATTERNS.find((x) => x.name === p);
      if (pat) addPattern(pat, at);
    } else if (f !== '') move(Number(f), at);
  }

  function applyJSON() {
    value = jsonDraft;
    if (!parseForm(value).error) selected = undefined;
  }
</script>

<div class="designer">
  <div class="bar">
    <div class="tabs" role="tablist">
      {#each [['design', 'Design'], ['preview', 'Preview'], ['json', 'JSON']] as [k, l] (k)}
        <button type="button" role="tab" class="small" class:on={view === k} aria-selected={view === k}
          disabled={k !== 'json' && !!parsed.error} onclick={() => {
            if (k === 'json') jsonDraft = value;
            view = k as typeof view;
          }}>{l}</button>
      {/each}
    </div>
    {#if parsed.error}<span class="err">Invalid JSON: {parsed.error}</span>{:else if count}<span class="err">{count} issue{count > 1 ? 's' : ''}</span>{/if}
  </div>

  {#if view === 'json' || parsed.error}
    <textarea id="{id}-json" class="mono" rows="14" bind:value={jsonDraft} {disabled} onblur={applyJSON}></textarea>
    <p class="hint">The form as the methodology stores it; applied when the text area loses focus.</p>
  {:else if view === 'preview'}
    <div class="preview">
      {#if fields.length}
        {#key value}<FormRenderer {form} onsubmit={() => {}} />{/key}
      {:else}
        <p class="hint">Add fields in the Design tab.</p>
      {/if}
    </div>
  {:else}
    <div class="cols">
      <aside class="palette" aria-label="Widgets and patterns">
        <h5>Widgets</h5>
        {#each palette() as w (w.name)}
          <button type="button" class="item" draggable={!disabled} title={w.description} {disabled}
            ondragstart={(e) => e.dataTransfer?.setData(WIDGET, w.name)} onclick={() => addWidget(w.name)}>
            {labelOf(w.name)}<span class="sub">{w.name}</span>
          </button>
        {/each}
        <h5>Patterns</h5>
        {#each PATTERNS as p (p.name)}
          <button type="button" class="item" draggable={!disabled} title={p.description} {disabled}
            ondragstart={(e) => e.dataTransfer?.setData(PATTERN, p.name)} onclick={() => addPattern(p)}>
            {p.label}<span class="sub">{(p.form.fields ?? []).length} fields</span>
          </button>
        {/each}
      </aside>

      <section class="canvas" aria-label="Fields of the form" role="list"
        ondragover={(e) => {
          if (disabled) return;
          e.preventDefault();
          if (!fields.length) dropAt = 0;
        }}
        ondragleave={(e) => {
          if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) dropAt = undefined;
        }}
        ondrop={drop}>
        <div class="formprops">
          <input type="text" class="title" placeholder="Title of the form" value={form.title ?? ''} {disabled} oninput={(e) => setForm({ title: e.currentTarget.value })} />
          <input type="text" placeholder="Description" value={form.description ?? ''} {disabled} oninput={(e) => setForm({ description: e.currentTarget.value })} />
        </div>
        {#each fields as f, i (i)}
          {#if dropAt === i}<div class="drop"></div>{/if}
          <div role="listitem" class="row" class:sel={selected === i} class:bad={!!issues[String(i)]} draggable={!disabled}
            ondragstart={(e) => e.dataTransfer?.setData(FIELD, String(i))} ondragover={(e) => over(e, i)}>
            <button type="button" class="pick" onclick={() => (selected = selected === i ? undefined : i)}>
              <span class="grip" aria-hidden="true">⋮⋮</span>
              <span class="lbl">{f.widget === 'markdown' ? 'Explanation' : f.label || f.name}{#if f.required}<span class="req"> *</span>{/if}</span>
              <span class="sub">{labelOf(f.widget)} · <code>{f.name}</code>{#if f.section} · {f.section}{/if}{#if f.visibleWhen?.length} · conditional{/if}{#if f.widget === 'group'} · {(f.fields ?? []).length} sub-fields{/if}</span>
              {#if issues[String(i)]}<span class="err">{issues[String(i)].length} issue{issues[String(i)].length > 1 ? 's' : ''}</span>{/if}
            </button>
            {#if !disabled}
              <button type="button" class="small" aria-label="Move up" disabled={i === 0} onclick={() => move(i, i - 1)}>↑</button>
              <button type="button" class="small" aria-label="Move down" disabled={i === fields.length - 1} onclick={() => move(i, i + 2)}>↓</button>
              <button type="button" class="small" aria-label="Remove {f.name}" onclick={() => remove(i)}>×</button>
            {/if}
          </div>
        {/each}
        {#if dropAt === fields.length}<div class="drop"></div>{/if}
        {#if !fields.length}<p class="empty">Drag a widget or a pattern here, or click it.</p>{/if}
        <div class="formprops two">
          <label>Submit button <input type="text" placeholder="Submit" value={form.submit ?? ''} {disabled} oninput={(e) => setForm({ submit: e.currentTarget.value })} /></label>
          <label title="Type of the artifact recording the values when the action has no code">Artifact type
            <input type="text" class="mono" placeholder="the action name" value={form.artifact ?? ''} {disabled} oninput={(e) => setForm({ artifact: e.currentTarget.value })} /></label>
        </div>
      </section>

      <aside class="side" aria-label="Field properties">
        {#if current && selected !== undefined}
          {#key selected}
            <FieldProperties {form} field={current} issues={issues[String(selected)]} {disabled} onchange={changeField} />
          {/key}
        {:else}
          <p class="hint">Select a field to edit its label, behaviour and constraints. Constraints limit what the person may choose; the engine checks them again.</p>
        {/if}
      </aside>
    </div>
  {/if}
</div>

<style>
  .designer {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.5rem;
    margin-bottom: 0.8rem;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 0.5rem;
  }
  .tabs {
    display: inline-flex;
  }
  .tabs button {
    border-radius: 0;
  }
  .tabs button:first-child {
    border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  }
  .tabs button:last-child {
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  }
  .tabs button.on {
    background: var(--surface-2);
    font-weight: 600;
  }
  .cols {
    display: grid;
    grid-template-columns: 11rem 1fr 18rem;
    gap: 0.6rem;
    align-items: start;
  }
  @media (max-width: 1100px) {
    .cols {
      grid-template-columns: 9rem 1fr;
    }
    .side {
      grid-column: 1 / -1;
    }
  }
  h5 {
    margin: 0.3rem 0;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: var(--muted);
  }
  .item {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    width: 100%;
    margin-bottom: 0.25rem;
    padding: 0.3rem 0.5rem;
    text-align: left;
    cursor: grab;
    font-weight: 500;
  }
  .sub {
    color: var(--muted);
    font-size: 0.75rem;
    font-weight: 400;
  }
  .canvas {
    min-height: 10rem;
    padding: 0.4rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
  }
  .formprops {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    margin-bottom: 0.5rem;
  }
  .formprops.two {
    flex-direction: row;
    margin: 0.5rem 0 0;
  }
  .formprops.two label {
    flex: 1;
    font-size: 0.8rem;
  }
  .formprops input {
    width: 100%;
  }
  .formprops .title {
    font-weight: 600;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    margin-bottom: 0.3rem;
    padding: 0.15rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    cursor: grab;
  }
  .row.sel {
    border-color: var(--accent, var(--warn));
    box-shadow: 0 0 0 2px var(--warn-soft);
  }
  .row.bad {
    border-color: var(--danger);
  }
  .pick {
    flex: 1;
    display: grid;
    grid-template-columns: auto auto 1fr auto;
    align-items: center;
    gap: 0.5rem;
    border: none;
    background: none;
    text-align: left;
    padding: 0.3rem;
    font-weight: 400;
    color: var(--text);
  }
  .grip {
    color: var(--muted);
  }
  .lbl {
    font-weight: 500;
  }
  .drop {
    height: 3px;
    margin: 0.15rem 0;
    border-radius: 2px;
    background: var(--warn);
  }
  .empty,
  .hint {
    color: var(--muted);
    font-size: 0.85rem;
  }
  .req,
  .err {
    color: var(--danger);
    font-size: 0.8rem;
  }
  textarea {
    width: 100%;
  }
  .preview {
    padding: 0.5rem;
  }
</style>
