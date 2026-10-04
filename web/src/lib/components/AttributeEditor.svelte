<script lang="ts">
  // The attributes of a node type or link type: code, label, type, widget, enum, default, section, tooltip, and the
  // property validators plugged on each one. What the user interface displays and edits a node from.
  import { emptyAttribute, moveItem, type AttributeForm } from '../methodologyForm';
  import RowTools from './RowTools.svelte';

  let {
    attrs = $bindable(),
    enums,
    validatorInstances = [],
    inherited = [],
    node = true,
    bad = () => false,
    readonly = false,
    path,
  }: {
    attrs: AttributeForm[];
    /** names of the enums of the domain */
    enums: string[];
    /** names of the property_validator instances of the domain */
    validatorInstances?: string[];
    /** names of the attributes inherited from the supertypes (an attribute of the same name overrides it) */
    inherited?: string[];
    /** a node type (an attribute may be the display name of the node) rather than a link type */
    node?: boolean;
    /** does an issue exist at this path? */
    bad?: (path: string) => boolean;
    readonly?: boolean;
    path: string;
  } = $props();

  const TYPES = ['', 'string', 'number', 'boolean', 'date', 'enum', 'json'];
  const WIDGETS = ['', 'text', 'textarea', 'dropdown', 'checkbox', 'date'];

  let open = $state<Record<string, boolean>>({});

  function add() {
    const a = emptyAttribute();
    attrs.push(a);
    open[a.uid] = true;
  }

  function setAsName(a: AttributeForm) {
    if (a.asName) for (const o of attrs) if (o !== a) o.asName = false;
  }
</script>

<div class="attrs" data-path="{path}.attributes">
  {#each attrs as a, i (a.uid)}
    {@const ap = `${path}.attributes[${i}]`}
    <div class="attr" class:has-issues={bad(ap)} data-path={ap}>
      <div class="row">
        <input type="text" class="mono" aria-label="Attribute code" bind:value={a.name} class:bad={bad(`${ap}.name`)} data-path="{ap}.name" placeholder="title" disabled={readonly} />
        <input type="text" aria-label="Label" bind:value={a.label} placeholder="Label" disabled={readonly} />
        <select aria-label="Type" bind:value={a.type} class:bad={bad(`${ap}.type`)} disabled={readonly}>
          {#each TYPES as t (t)}<option value={t}>{t || 'untyped'}</option>{/each}
        </select>
        <select aria-label="Widget" bind:value={a.widget} class:bad={bad(`${ap}.widget`)} disabled={readonly}>
          {#each WIDGETS as w (w)}<option value={w}>{w || 'default widget'}</option>{/each}
        </select>
        {#if a.type === 'enum'}
          <select aria-label="Enum" bind:value={a.enum} class:bad={bad(`${ap}.enum`)} data-path="{ap}.enum" disabled={readonly}>
            <option value="">— enum —</option>
            {#if a.enum && !enums.includes(a.enum)}<option value={a.enum}>{a.enum} (unknown)</option>{/if}
            {#each enums as e (e)}<option value={e}>{e}</option>{/each}
          </select>
        {/if}
        <button type="button" class="small ghost" aria-expanded={!!open[a.uid]} onclick={() => (open[a.uid] = !open[a.uid])}>
          {open[a.uid] ? '▾' : '▸'} {a.validators.length ? `${a.validators.length} validator${a.validators.length > 1 ? 's' : ''}` : 'more'}
        </button>
        {#if !readonly}
          <RowTools index={i} count={attrs.length} label="the attribute" onmove={(delta) => moveItem(attrs, i, delta)} onremove={() => attrs.splice(i, 1)} />
        {/if}
      </div>
      {#if inherited.includes(a.name.trim())}<span class="hint">overrides the inherited attribute</span>{/if}
      {#if open[a.uid]}
        <div class="more">
          <div class="grid">
            <div class="field">
              <label for="{ap}-sec">Section</label>
              <input id="{ap}-sec" type="text" bind:value={a.section} placeholder="General" disabled={readonly} />
            </div>
            <div class="field">
              <label for="{ap}-ord">Order</label>
              <input id="{ap}-ord" type="number" bind:value={a.order} disabled={readonly} />
            </div>
            <div class="field">
              <label for="{ap}-def">Default</label>
              <input id="{ap}-def" type="text" bind:value={a.default} disabled={readonly} />
            </div>
          </div>
          <div class="field">
            <label for="{ap}-tip">Tooltip</label>
            <input id="{ap}-tip" type="text" bind:value={a.tooltip} disabled={readonly} />
          </div>
          <div class="field">
            <label for="{ap}-desc">Description</label>
            <input id="{ap}-desc" type="text" bind:value={a.description} disabled={readonly} />
          </div>
          {#if node}
            <label class="check" title="The attribute is the display name of the node"><input type="checkbox" bind:checked={a.asName} onchange={() => setAsName(a)} disabled={readonly} /> display name of the node</label>
          {/if}
          <div class="validators" data-path="{ap}.validators">
            <span class="label">Validators <span class="hint">(property validators run in this order when a node is created or modified)</span></span>
            {#each a.validators as v, k}
              <div class="vrow" data-path="{ap}.validators[{k}]">
                <select aria-label="Validator instance" bind:value={a.validators[k]} class:bad={bad(`${ap}.validators[${k}]`)} disabled={readonly}>
                  <option value="">— validator —</option>
                  {#if v && !validatorInstances.includes(v)}<option value={v}>{v} (unknown)</option>{/if}
                  {#each validatorInstances as inst (inst)}<option value={inst}>{inst}</option>{/each}
                </select>
                {#if !readonly}
                  <RowTools index={k} count={a.validators.length} label="the validator" onmove={(delta) => moveItem(a.validators, k, delta)} onremove={() => a.validators.splice(k, 1)} />
                {/if}
              </div>
            {/each}
            {#if !readonly}
              <button type="button" class="small" disabled={!validatorInstances.length} title={validatorInstances.length ? '' : 'Create a property_validator instance first (Algorithms)'} onclick={() => a.validators.push('')}>+ Validator</button>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  {:else}
    <p class="empty">No attributes.</p>
  {/each}
  {#if !readonly}<button type="button" class="small" onclick={add}>+ Attribute</button>{/if}
</div>

<style>
  .attr {
    margin-bottom: 0.3rem;
    border-radius: var(--radius-sm);
  }
  .attr.has-issues {
    box-shadow: inset 3px 0 0 var(--danger);
    padding-left: 5px;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(110px, 1fr) minmax(110px, 1fr) 7rem 8.5rem auto auto auto;
    gap: 0.4rem;
    align-items: center;
  }
  .more {
    display: grid;
    gap: 0.4rem;
    margin: 0.3rem 0 0.6rem 0.6rem;
    padding-left: 0.6rem;
    border-left: 2px solid var(--border);
  }
  .vrow {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    margin: 0.2rem 0;
  }
  .vrow select {
    width: auto;
    min-width: 11rem;
  }
  .label {
    font-size: 0.85rem;
    font-weight: 600;
  }
  .check {
    display: inline-flex;
    gap: 0.3rem;
    align-items: center;
    font-weight: 400;
  }
  @media (max-width: 800px) {
    .row {
      grid-template-columns: 1fr;
    }
  }
</style>
