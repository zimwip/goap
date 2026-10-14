<script lang="ts">
  // The properties of the field selected in the designer: identity, behaviour, then the constraints its widget declares,
  // with pickers from the type catalogue for node types, link types and states.
  import type { FieldDef, FormDef } from '../types';
  import { changeWidget, constraintsOf, labelOf, palette, setConstraint } from '../design';
  import { strings } from '../validate';
  import { typeCatalog } from '../../stores/types.svelte';
  import StringList from './StringList.svelte';

  let {
    form,
    field,
    issues = [],
    disabled = false,
    onchange,
  }: { form: FormDef; field: FieldDef; issues?: string[]; disabled?: boolean; onchange: (f: FieldDef) => void } = $props();

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
    }
    return [];
  }

  const set = (patch: Partial<FieldDef>) => onchange({ ...field, ...patch });
  const setC = (name: string, v: unknown) => onchange(setConstraint(field, name, v));
  const others = (widget?: string) => (form.fields ?? []).filter((f) => f !== field && f.name && (!widget || f.widget === widget));
  const scalarDefault = $derived(['text', 'textarea', 'number', 'boolean', 'date', 'select'].includes(field.widget));
</script>

<div class="props">
  <div class="field">
    <label for={id('widget')}>Widget</label>
    <select id={id('widget')} value={field.widget} {disabled} onchange={(e) => onchange(changeWidget(field, e.currentTarget.value))}>
      {#each palette() as w (w.name)}<option value={w.name}>{labelOf(w.name)} ({w.name})</option>{/each}
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
              {#each others(c.widget) as f (f.name)}<option value={f.name}>{f.label || f.name} ({f.name})</option>{/each}
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
</style>
