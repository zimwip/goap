<script lang="ts">
  // What a node type says beyond its properties: its lifecycle (by name), the
  // node types it embeds when it is a document, and whether it is change controlled.
  import { moveItem, type NodeTypeForm } from '../methodologyForm';
  import RowTools from './RowTools.svelte';

  let {
    n = $bindable(),
    lifecycles,
    typeNames,
    inherited,
    properties = [],
    validatorInstances = [],
    bad = () => false,
    readonly = false,
    path,
  }: {
    n: NodeTypeForm;
    lifecycles: string[];
    typeNames: string[];
    /** ancestor whose lifecycle applies when the type names none */
    inherited?: { type: string; lifecycle: string };
    /** properties of the type, own and inherited: what validators can be plugged on */
    properties?: string[];
    /** names of the property_validator instances of the domain */
    validatorInstances?: string[];
    /** does an issue exist at this path? */
    bad?: (path: string) => boolean;
    readonly?: boolean;
    path: string;
  } = $props();
</script>

<details class="meta" open={!!n.lifecycle || !!n.document || !n.changeControlled || n.validators.length > 0}>
  <summary>
    Lifecycle, documents &amp; validators
    {#if n.lifecycle}<span class="tag">lifecycle: {n.lifecycle}</span>{:else if inherited}<span class="tag muted">inherits {inherited.lifecycle} from {inherited.type}</span>{/if}
    {#if n.document}<span class="tag">document</span>{/if}
    {#if !n.changeControlled}<span class="tag muted">direct writes</span>{/if}
    {#if n.validators.length}<span class="tag">{n.validators.length} validator{n.validators.length > 1 ? 's' : ''}</span>{/if}
  </summary>
  <div class="body" data-path="{path}.lifecycle">
    <div class="line">
      <label for="{path}-lc">Lifecycle</label>
      <select id="{path}-lc" bind:value={n.lifecycle} disabled={readonly}>
        <option value="">{inherited ? `— inherit (${inherited.lifecycle}) —` : '— none —'}</option>
        {#if n.lifecycle && !lifecycles.includes(n.lifecycle)}<option value={n.lifecycle}>{n.lifecycle} (unknown)</option>{/if}
        {#each lifecycles as l (l)}<option value={l}>{l}</option>{/each}
      </select>
      <label class="check" title="Off: the nodes are written directly, outside changes (no lifecycle)"><input type="checkbox" bind:checked={n.changeControlled} disabled={readonly || !!n.lifecycle} /> modified through changes only</label>
    </div>
    <div class="field">
      <label for="{path}-doc">Embedded node types <span class="hint">(a document: comma-separated, attached by outgoing “contains” links)</span></label>
      <input id="{path}-doc" type="text" class="mono" bind:value={n.document} placeholder="Requirement, Chapter" list="{path}-types" disabled={readonly} />
      <datalist id="{path}-types">{#each typeNames as t (t)}<option value={t}></option>{/each}</datalist>
    </div>
    <div class="validators" data-path="{path}.validators">
      <span class="label">Property validators <span class="hint">(algorithms run in this order on create / update, after those of the supertypes)</span></span>
      {#each n.validators as v, k}
        <div class="vrow" data-path="{path}.validators[{k}]">
          <select aria-label="Property" bind:value={v.property} class:bad={bad(`${path}.validators[${k}].property`)} disabled={readonly}>
            <option value="">— property —</option>
            {#if v.property && !properties.includes(v.property)}<option value={v.property}>{v.property} (unknown)</option>{/if}
            {#each properties as p (p)}<option value={p}>{p}</option>{/each}
          </select>
          <select aria-label="Validator instance" bind:value={v.instance} class:bad={bad(`${path}.validators[${k}].instance`)} data-path="{path}.validators[{k}].instance" disabled={readonly}>
            <option value="">— validator —</option>
            {#if v.instance && !validatorInstances.includes(v.instance)}<option value={v.instance}>{v.instance} (unknown)</option>{/if}
            {#each validatorInstances as i (i)}<option value={i}>{i}</option>{/each}
          </select>
          {#if !readonly}
            <RowTools index={k} count={n.validators.length} label="the validator" onmove={(delta) => moveItem(n.validators, k, delta)} onremove={() => n.validators.splice(k, 1)} />
          {/if}
        </div>
      {/each}
      {#if !readonly}
        <button type="button" class="small" disabled={!validatorInstances.length} title={validatorInstances.length ? '' : 'Create a property_validator instance in the Algorithms section first'} onclick={() => n.validators.push({ property: properties[0] ?? '', instance: '' })}>+ Validator</button>
      {/if}
    </div>
  </div>
</details>

<style>
  .meta {
    margin: 0.1rem 0 0.6rem 0.6rem;
    border-left: 2px solid var(--border);
    padding-left: 0.6rem;
  }
  summary {
    cursor: pointer;
    font-size: 0.9rem;
    color: var(--muted);
  }
  .tag {
    margin-left: 0.4rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.75rem;
    color: var(--text);
  }
  .tag.muted {
    color: var(--muted);
  }
  .body {
    display: grid;
    gap: 0.4rem;
    padding: 0.4rem 0;
  }
  .line {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    flex-wrap: wrap;
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
  .line select {
    width: auto;
    min-width: 12rem;
  }
  .check {
    display: inline-flex;
    gap: 0.3rem;
    align-items: center;
    font-weight: 400;
  }
</style>
