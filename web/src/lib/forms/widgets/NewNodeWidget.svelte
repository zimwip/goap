<script lang="ts">
  // A node the change creates (or several): its type among those the constraints allow, its key and the attributes
  // asked (the name of the type when none is listed).
  import type { WidgetProps } from '../registry';
  import type { NewNodeValue } from '../types';
  import { strings } from '../validate';
  import { typeCatalog, typeName } from '../../stores/types.svelte';
  import { attributeView, parseValue, valueText, type AttributeView } from '../../attributes';

  let { id, field, value, onchange, disabled, invalid }: WidgetProps = $props();
  const multiple = $derived(field.constraints?.['multiple'] === true);
  const entries = $derived<NewNodeValue[]>(multiple ? (Array.isArray(value) ? (value as NewNodeValue[]) : []) : value && typeof value === 'object' ? [value as NewNodeValue] : [{}]);
  const cat = $derived(typeCatalog.cat);
  const declared = $derived(strings(field.constraints?.['types']));
  const types = $derived(declared.length ? [...new Set(declared.flatMap((t) => cat.withSubtypes(t)))].sort() : cat.names());
  const asked = $derived(strings(field.constraints?.['properties']));
  const max = $derived(typeof field.constraints?.['max'] === 'number' ? (field.constraints['max'] as number) : Infinity);

  /** the attributes asked of a node of the type */
  function attributesOf(type: string | undefined): AttributeView[] {
    const all = cat.attributes(type).map(attributeView);
    if (asked.length) return asked.map((n) => all.find((a) => a.name === n) ?? attributeView({ attribute: { name: n } }));
    const named = all.find((a) => a.asName);
    return named ? [named] : [];
  }

  function emit(list: NewNodeValue[]) {
    onchange(multiple ? list : list[0]);
  }
  function put(i: number, patch: NewNodeValue) {
    const next = entries.map((e, j) => (j === i ? { ...e, ...patch } : e));
    emit(next);
  }
  function setProp(i: number, a: AttributeView, raw: string) {
    const props = { ...(entries[i].props ?? {}) };
    let v: unknown = raw;
    try {
      v = parseValue(a, raw);
    } catch {
      /* kept as typed: the check says what is wrong */
    }
    if (v === '' || v === undefined) delete props[a.name];
    else props[a.name] = v;
    put(i, { props: Object.keys(props).length ? props : undefined });
  }
  const first = (i: number): NewNodeValue => (types.length === 1 && !entries[i]?.type ? { type: types[0] } : {});
</script>

<div class="newnodes" {id}>
  {#each entries as n, i (i)}
    {@const t = n.type || first(i).type}
    <div class="entry" class:boxed={multiple}>
      <div class="row">
        <select aria-label="Type" value={t ?? ''} {disabled} aria-invalid={invalid} onchange={(e) => put(i, { type: e.currentTarget.value || undefined, props: undefined })}>
          <option value="">type…</option>
          {#each types as ty (ty)}<option value={ty}>{typeName(ty)}</option>{/each}
        </select>
        <input type="text" class="mono" aria-label="Key" placeholder="key, e.g. REQ-42" value={n.key ?? ''} {disabled} aria-invalid={invalid}
          oninput={(e) => put(i, { ...(t && !n.type ? { type: t } : {}), key: e.currentTarget.value.trim() || undefined })} />
        {#if multiple}<button type="button" class="small" aria-label={`Remove node ${i + 1}`} {disabled} onclick={() => emit(entries.filter((_, j) => j !== i))}>×</button>{/if}
      </div>
      {#each attributesOf(t) as a (a.name)}
        {@const v = n.props?.[a.name]}
        <label class="attr">
          <span>{a.label}</span>
          {#if a.type === 'enum'}
            <select value={valueText(v)} {disabled} onchange={(e) => setProp(i, a, e.currentTarget.value)}>
              <option value="">—</option>
              {#each a.values as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
            </select>
          {:else if a.type === 'boolean'}
            <input type="checkbox" checked={v === true} {disabled} onchange={(e) => setProp(i, a, e.currentTarget.checked ? 'true' : '')} />
          {:else if a.widget === 'textarea' || a.type === 'text'}
            <textarea rows="2" value={valueText(v)} {disabled} oninput={(e) => setProp(i, a, e.currentTarget.value)}></textarea>
          {:else}
            <input type={a.type === 'number' ? 'number' : a.type === 'date' ? 'date' : 'text'} value={valueText(v)} {disabled}
              oninput={(e) => setProp(i, a, e.currentTarget.value)} />
          {/if}
        </label>
      {/each}
    </div>
  {/each}
  {#if multiple}
    <button type="button" class="small" disabled={disabled || entries.length >= max} onclick={() => emit([...entries, types.length === 1 ? { type: types[0] } : {}])}>+ Add a node</button>
  {/if}
</div>

<style>
  .newnodes {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    align-items: flex-start;
  }
  .entry {
    align-self: stretch;
  }
  .entry.boxed {
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.4rem;
  }
  .row {
    display: flex;
    gap: 0.3rem;
  }
  .row select {
    width: auto !important;
    max-width: 12rem;
  }
  .attr {
    display: block;
    margin-top: 0.3rem;
    font-weight: 400;
  }
  .attr span {
    display: block;
    font-size: 0.8rem;
    color: var(--muted);
  }
</style>
