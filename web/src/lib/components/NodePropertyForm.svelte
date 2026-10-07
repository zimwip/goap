<script lang="ts">
  import { untrack } from 'svelte';
  import { orderedAttributes, parseValue as parseAttr, valueText, type AttributeView } from '../attributes';
  import type { AttributeInfo } from '../api';
  import { assistField, type FieldSpec } from '../helper/fields.svelte';
  // Edition of the properties of a node: the attributes its type defines (laid out by section, edited with
  // their widget), the other properties it has, and new ones. Only the changed values are returned.
  let {
    props,
    attributes = [],
    declared,
    typeName,
    open = false,
    busy = false,
    onsave,
    oncancel,
  }: {
    props: Record<string, unknown>;
    /** the attributes of the type of the node */
    attributes?: AttributeInfo[];
    declared: string[];
    typeName: string;
    /** the type is free-form (`additionalProperties`): properties that are no attribute may be added */
    open?: boolean;
    busy?: boolean;
    onsave: (patch: Record<string, unknown>) => Promise<boolean> | boolean;
    oncancel: () => void;
  } = $props();

  const text = valueText;
  const attrs = $derived(orderedAttributes(attributes));
  const attrOf = (k: string): AttributeView | undefined => attrs.find((a) => a.name === k);
  const sections = $derived([...new Set(attrs.map((a) => a.section))]);
  // the form is created each time it is opened: it starts from the values of that moment
  const start = untrack(() =>
    Object.fromEntries(
      [...new Set([...declared, ...(open ? Object.keys(props) : [])])].map((k) => {
        const a = orderedAttributes(attributes).find((x) => x.name === k);
        return [k, props[k] === undefined && a ? a.default : text(props[k])];
      }),
    ),
  );
  let draft = $state<Record<string, string>>(start);
  const others = $derived(Object.keys(draft).filter((k) => !attrOf(k)));
  let newKey = $state('');
  let newValue = $state('');
  let error = $state('');

  /** What the contextual helper (ADR 0086) may fill: the attribute, read and set through the form's draft like a typed value. */
  function helperField(a: AttributeView): FieldSpec {
    return {
      id: a.name,
      label: a.label,
      type: a.type || undefined,
      enum: a.type === 'enum' ? a.values.map((v) => v.value) : undefined,
      description: a.tooltip || undefined,
      get: () => {
        try {
          return parseAttr(a, draft[a.name] ?? '');
        } catch {
          return draft[a.name];
        }
      },
      set: (v) => (draft[a.name] = text(v)),
    };
  }

  /** A value typed in the form: text, unless the property already holds a non-text value. */
  function parse(key: string, value: string): unknown {
    const a = attrOf(key);
    if (a && a.type) return parseAttr(a, value);
    const cur = props[key];
    if (cur !== undefined && cur !== null && typeof cur !== 'string') {
      try {
        return JSON.parse(value);
      } catch {
        throw new Error(`“${key}” holds ${typeof cur === 'object' ? 'structured' : typeof cur} data: ${value} is not valid JSON`);
      }
    }
    return value;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = '';
    const patch: Record<string, unknown> = {};
    try {
      for (const [k, v] of Object.entries(draft)) if (v !== text(props[k])) patch[k] = parse(k, v);
      const nk = open ? newKey.trim() : '';
      if (nk) {
        if (nk in draft) throw new Error(`property “${nk}” is already in the form`);
        patch[nk] = newValue;
      }
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      return;
    }
    if (!Object.keys(patch).length) return oncancel();
    if (await onsave(patch)) oncancel();
  }
</script>

<form class="form" onsubmit={submit}>
  {#each sections as sec (sec)}
    {#if sec && attrs.some((a) => a.section === sec)}<h4 class="section">{sec}</h4>{/if}
    {#each attrs.filter((a) => a.section === sec) as a (a.name)}
      <div class="field">
        <label for="np-{a.name}" title={a.tooltip}>{a.label}{#if a.label !== a.name} <span class="hint mono">{a.name}</span>{/if}</label>
        {#if a.widget === 'checkbox'}
          <input id="np-{a.name}" use:assistField={helperField(a)} type="checkbox" checked={draft[a.name] === 'true'} onchange={(e) => (draft[a.name] = e.currentTarget.checked ? 'true' : 'false')} />
        {:else if a.widget === 'dropdown'}
          <select id="np-{a.name}" use:assistField={helperField(a)} bind:value={draft[a.name]}>
            <option value="">—</option>
            {#if draft[a.name] && !a.values.some((v) => v.value === draft[a.name])}<option value={draft[a.name]}>{draft[a.name]} (not in {a.enum || 'the list'})</option>{/if}
            {#each a.values as v (v.value)}<option value={v.value}>{v.label}</option>{/each}
          </select>
        {:else if a.widget === 'date'}
          <input id="np-{a.name}" use:assistField={helperField(a)} type="date" bind:value={draft[a.name]} />
        {:else if a.widget === 'textarea' || draft[a.name].length > 80 || draft[a.name].includes('\n')}
          <textarea id="np-{a.name}" use:assistField={helperField(a)} rows="4" bind:value={draft[a.name]}></textarea>
        {:else}
          <input id="np-{a.name}" use:assistField={helperField(a)} type={a.type === 'number' ? 'number' : 'text'} step={a.type === 'number' ? 'any' : undefined} bind:value={draft[a.name]} />
        {/if}
        {#if a.validators.length}<span class="hint">Checked by {a.validators.join(', ')}</span>{/if}
      </div>
    {/each}
  {/each}
  {#each others as k (k)}
    <div class="field">
      <label for="np-{k}">{k}{#if !declared.includes(k)} <span class="hint">(not declared by {typeName})</span>{/if}</label>
      {#if draft[k].length > 80 || draft[k].includes('\n')}
        <textarea id="np-{k}" rows="4" bind:value={draft[k]}></textarea>
      {:else}
        <input id="np-{k}" type="text" bind:value={draft[k]} />
      {/if}
    </div>
  {/each}
  {#if open}
    <div class="newprop">
      <input type="text" class="mono" placeholder="new property" aria-label="New property name" bind:value={newKey} />
      <input type="text" placeholder="value" aria-label="New property value" bind:value={newValue} />
    </div>
  {/if}
  <p class="hint">Values are text; a property that already holds a number, boolean or list is edited as JSON. Only the changed properties are proposed.{#if !open} Only the attributes of {typeName} can be set.{/if}</p>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="row">
    <button type="submit" class="primary" disabled={busy}>{busy ? 'Proposing…' : 'Propose the changes'}</button>
    <button type="button" onclick={oncancel}>Cancel</button>
  </div>
</form>

<style>
  .form {
    display: grid;
    gap: 0.5rem;
    max-width: 52rem;
  }
  .section {
    margin: 0.6rem 0 0;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
  }
  .newprop {
    display: grid;
    grid-template-columns: minmax(8rem, 1fr) 3fr;
    gap: 0.4rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
  }
</style>
