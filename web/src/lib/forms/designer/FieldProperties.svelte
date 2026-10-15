<script lang="ts">
  // The properties of the field selected in the designer: identity, behaviour, then the constraints its widget declares,
  // with pickers from the type catalogue for node types, link types and states.
  import type { FieldDef, FormDef } from '../types';
  import { NOT_NESTED, changeWidget, constraintsOf, designIssues, insertField, labelOf, moveField, newField, palette, removeField, setConstraint, updateField } from '../design';
  import { strings } from '../validate';
  import { typeCatalog } from '../../stores/types.svelte';
  import StringList from './StringList.svelte';
  import ConditionsEditor from './ConditionsEditor.svelte';
  import Self from './FieldProperties.svelte';

  let {
    form,
    field,
    issues = [],
    disabled = false,
    onchange,
    nested = false,
  }: {
    /** the fields the field is one of (a group's sub-fields for a sub-field) */
    form: FormDef;
    field: FieldDef;
    issues?: string[];
    disabled?: boolean;
    onchange: (f: FieldDef) => void;
    /** a sub-field of a group: no group nor link offered */
    nested?: boolean;
  } = $props();

  const uid = Math.random().toString(36).slice(2, 8);
  const id = (k: string) => `fp-${uid}-${k}`;
  const specs = $derived(constraintsOf(field.widget));
  const cat = $derived(typeCatalog.cat);
  const nodeTypes = $derived(cat.names());
  const linkTypes = $derived(cat.linkNames());
  const display = $derived(field.widget === 'markdown');

  /** the states of the lifecycles of the types a field names (all the lifecycles' when it names none) */
  function statesOf(types: string[]): string[] {
    const refs = types.length ? types : nodeTypes;
    const out = new Set<string>();
    for (const t of refs) for (const s of cat.lifecycle(t)?.states ?? []) if (s.name) out.add(s.name);
    return [...out].sort();
  }

  function suggestions(name: string): string[] {
    const c = field.constraints ?? {};
    switch (name) {
      case 'types':
      case 'toTypes':
        return nodeTypes;
      case 'linkTypes': {
        const from = typeof c['from'] === 'string' ? form.fields?.find((f) => f.name === c['from']) : undefined;
        const types = strings(from?.constraints?.['types']);
        return types.length ? [...new Set(types.flatMap((t) => cat.linksFrom(t).map((l) => l.ref ?? '')))].filter(Boolean).sort() : linkTypes;
      }
      case 'states':
        return statesOf(strings(c['types']));
      case 'toStates':
        return statesOf(strings(c['toTypes']));
      case 'namespace':
        return Object.keys(cat.domains).sort();
      case 'properties': {
        const types = strings(c['types']);
        return [...new Set((types.length ? types : nodeTypes).flatMap((t) => cat.properties(t)))].sort();
      }
      case 'accept':
        return ['text/*', 'image/*', 'application/pdf', 'application/json', '.csv', '.md', '.txt', '.xlsx', '.docx'];
    }
    return [];
  }

  const set = (patch: Partial<FieldDef>) => onchange({ ...field, ...patch });
  const setC = (name: string, v: unknown) => onchange(setConstraint(field, name, v));
  const others = (widgets?: string[]) => (form.fields ?? []).filter((f) => f !== field && f.name && (!widgets?.length || widgets.includes(f.widget)));
  /** the fields before this one: what its conditions may test */
  const earlier = $derived((form.fields ?? []).slice(0, Math.max(0, (form.fields ?? []).indexOf(field))));
  const widgets = $derived(palette().filter((w) => !nested || !NOT_NESTED.includes(w.name)));

  // the sub-fields of a group, edited as a small form of their own
  let sub = $state<number | undefined>(undefined);
  const subForm = $derived<FormDef>({ fields: field.fields ?? [] });
  const subIssues = $derived(designIssues(subForm));
  const setSub = (next: FormDef) => set({ fields: next.fields });
  const scalarDefault = $derived(['text', 'textarea', 'number', 'boolean', 'date', 'select'].includes(field.widget));
</script>

<div class="props">
  <div class="field">
    <label for={id('widget')}>Widget</label>
    <select id={id('widget')} value={field.widget} {disabled} onchange={(e) => onchange(changeWidget(field, e.currentTarget.value))}>
      {#each widgets as w (w.name)}<option value={w.name}>{labelOf(w.name)} ({w.name})</option>{/each}
    </select>
  </div>
  <div class="grid2">
    <div class="field">
      <label for={id('name')}>Name <span class="opt">(key of the value)</span></label>
      <input id={id('name')} class="mono" type="text" value={field.name} {disabled} onchange={(e) => set({ name: e.currentTarget.value.trim() })} />
    </div>
    {#if !display}
      <div class="field">
        <label for={id('label')}>Label</label>
        <input id={id('label')} type="text" value={field.label ?? ''} {disabled} oninput={(e) => set({ label: e.currentTarget.value || undefined })} />
      </div>
    {/if}
  </div>
  {#if !display}
    <div class="field">
      <label for={id('help')}>Help</label>
      <input id={id('help')} type="text" value={field.help ?? ''} {disabled} oninput={(e) => set({ help: e.currentTarget.value || undefined })} />
    </div>
    <div class="grid2">
      <div class="field">
        <label for={id('ph')}>Placeholder</label>
        <input id={id('ph')} type="text" value={field.placeholder ?? ''} {disabled} oninput={(e) => set({ placeholder: e.currentTarget.value || undefined })} />
      </div>
      <div class="field">
        <label for={id('section')}>Section</label>
        <input id={id('section')} type="text" value={field.section ?? ''} {disabled} oninput={(e) => set({ section: e.currentTarget.value || undefined })} />
      </div>
    </div>
    <label class="check"><input type="checkbox" checked={field.required === true} {disabled} onchange={(e) => set({ required: e.currentTarget.checked || undefined })} /> Required</label>
    <ConditionsEditor id={id('vw')} label="Shown when" fields={earlier} value={field.visibleWhen} {disabled} onchange={(v) => set({ visibleWhen: v })} />
    {#if !field.required}
      <ConditionsEditor id={id('rw')} label="Required when" fields={earlier} value={field.requiredWhen} {disabled} onchange={(v) => set({ requiredWhen: v })} />
    {/if}
    {#if scalarDefault}
      <div class="field">
        <label for={id('default')}>Default</label>
        {#if field.widget === 'boolean'}
          <select id={id('default')} value={field.default === undefined ? '' : String(field.default)} {disabled}
            onchange={(e) => set({ default: e.currentTarget.value === '' ? undefined : e.currentTarget.value === 'true' })}>
            <option value="">none</option><option value="true">yes</option><option value="false">no</option>
          </select>
        {:else if field.widget === 'select'}
          <select id={id('default')} value={typeof field.default === 'string' ? field.default : ''} {disabled}
            onchange={(e) => set({ default: e.currentTarget.value || undefined })}>
            <option value="">none</option>
            {#each strings(field.constraints?.['options']) as o (o)}<option value={o}>{o}</option>{/each}
          </select>
        {:else}
          <input id={id('default')} type={field.widget === 'number' ? 'number' : field.widget === 'date' ? 'date' : 'text'}
            value={field.default === undefined ? '' : String(field.default)} {disabled}
            onchange={(e) => {
              const v = e.currentTarget.value;
              set({ default: v === '' ? undefined : field.widget === 'number' ? Number(v) : v });
            }} />
        {/if}
      </div>
    {/if}
  {/if}

  {#if field.widget === 'group'}
    <h5>Sub-fields</h5>
    {#each subForm.fields ?? [] as f, i (i)}
      <div class="subrow" class:bad={!!subIssues[String(i)]}>
        <button type="button" class="pick" class:on={sub === i} onclick={() => (sub = sub === i ? undefined : i)}>
          {f.label || f.name}{#if f.required}<span class="req"> *</span>{/if} <span class="opt">{labelOf(f.widget)}</span>
        </button>
        {#if !disabled}
          <button type="button" class="small" aria-label="Move up" disabled={i === 0} onclick={() => { setSub(moveField(subForm, i, i - 1)); sub = undefined; }}>↑</button>
          <button type="button" class="small" aria-label="Remove {f.name}" onclick={() => { setSub(removeField(subForm, i)); sub = undefined; }}>×</button>
        {/if}
      </div>
      {#if sub === i}
        <div class="nested">
          {#key i}
            <Self form={subForm} field={f} nested issues={subIssues[String(i)]} {disabled} onchange={(g) => setSub(updateField(subForm, i, g))} />
          {/key}
        </div>
      {/if}
    {/each}
    {#if !disabled}
      <select aria-label="Add a sub-field" value="" onchange={(e) => {
        const w = e.currentTarget.value;
        e.currentTarget.value = '';
        if (w) { setSub(insertField(subForm, newField(subForm, w))); sub = (subForm.fields ?? []).length; }
      }}>
        <option value="">+ sub-field…</option>
        {#each palette().filter((w) => !NOT_NESTED.includes(w.name)) as w (w.name)}<option value={w.name}>{labelOf(w.name)}</option>{/each}
      </select>
    {/if}
  {/if}

  {#if specs.length}
    <h5>Constraints</h5>
    {#each specs as c (c.name)}
      {@const v = field.constraints?.[c.name]}
      <div class="field">
        {#if c.type === 'boolean'}
          <label class="check"><input id={id(c.name)} type="checkbox" checked={v === true} {disabled} onchange={(e) => setC(c.name, e.currentTarget.checked || undefined)} />
            {c.name}{#if c.description}<span class="opt">&nbsp;— {c.description}</span>{/if}</label>
        {:else}
          <label for={id(c.name)}>{c.name}{#if c.required}<span class="req"> *</span>{/if}{#if c.description}<span class="opt">&nbsp;— {c.description}</span>{/if}</label>
          {#if c.type === 'strings'}
            <StringList id={id(c.name)} value={strings(v)} suggestions={suggestions(c.name)} {disabled} onchange={(l) => setC(c.name, l)} />
          {:else if c.type === 'number'}
            <input id={id(c.name)} type="number" value={typeof v === 'number' ? v : ''} {disabled}
              onchange={(e) => setC(c.name, e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value))} />
          {:else if c.type === 'field'}
            <select id={id(c.name)} value={typeof v === 'string' ? v : ''} {disabled} onchange={(e) => setC(c.name, e.currentTarget.value || undefined)}>
              <option value="">none</option>
              {#each others(c.widgets) as f (f.name)}<option value={f.name}>{f.label || f.name} ({f.name})</option>{/each}
            </select>
          {:else if field.widget === 'markdown' && c.name === 'text'}
            <textarea id={id(c.name)} rows="4" value={typeof v === 'string' ? v : ''} {disabled} oninput={(e) => setC(c.name, e.currentTarget.value)}></textarea>
          {:else}
            {@const sugg = suggestions(c.name)}
            <input id={id(c.name)} type="text" class="mono" list={sugg.length ? id(`${c.name}-list`) : undefined} value={typeof v === 'string' ? v : ''} {disabled}
              onchange={(e) => setC(c.name, e.currentTarget.value.trim() || undefined)} />
            {#if sugg.length}<datalist id={id(`${c.name}-list`)}>{#each sugg as s (s)}<option value={s}></option>{/each}</datalist>{/if}
          {/if}
        {/if}
      </div>
    {/each}
  {/if}
  {#each issues as msg (msg)}<p class="issue">{msg}</p>{/each}
</div>

<style>
  .props :global(input:not([type='checkbox'])),
  .props select,
  .props textarea {
    width: 100%;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
  }
  .field {
    margin-bottom: 0.5rem;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-weight: 400;
    margin: 0 0 0.5rem;
  }
  h5 {
    margin: 0.8rem 0 0.4rem;
    font-size: 0.8rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: var(--muted);
  }
  .opt {
    color: var(--muted);
    font-weight: 400;
    font-size: 0.8rem;
  }
  .req,
  .issue {
    color: var(--danger);
  }
  .issue {
    font-size: 0.8rem;
    margin: 0.2rem 0;
  }
  .subrow {
    display: flex;
    gap: 0.25rem;
    margin-bottom: 0.25rem;
  }
  .subrow .pick {
    flex: 1;
    text-align: left;
    font-weight: 400;
  }
  .subrow .pick.on {
    font-weight: 600;
  }
  .subrow.bad .pick {
    border-color: var(--danger);
  }
  .nested {
    border-left: 2px solid var(--border);
    padding-left: 0.5rem;
    margin: 0.3rem 0 0.6rem;
  }
</style>
