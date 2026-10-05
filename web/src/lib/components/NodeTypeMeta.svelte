<script lang="ts">
  // What a node type says beyond its attributes: its lifecycle (by name), the
  // node types it embeds when it is a document, whether it is change controlled,
  // and the editor the user interface opens its nodes with.
  import { moveItem, type NodeTypeForm } from '../methodologyForm';
  import { nodeEditorNames } from '../shell/registry';
  import RowTools from './RowTools.svelte';

  let {
    n = $bindable(),
    lifecycles,
    typeNames,
    inherited,
    validatorInstances = [],
    onopenLifecycle,
    bad = () => false,
    readonly = false,
    path,
  }: {
    n: NodeTypeForm;
    lifecycles: string[];
    typeNames: string[];
    /** ancestor whose lifecycle applies when the type names none */
    inherited?: { type: string; lifecycle: string };
    /** names of the node_validator instances of the domain */
    validatorInstances?: string[];
    /** opens the editor of a lifecycle, by name */
    onopenLifecycle?: (name: string) => void;
    /** does an issue exist at this path? */
    bad?: (path: string) => boolean;
    readonly?: boolean;
    path: string;
  } = $props();
</script>

<details class="meta" open={!!n.lifecycle || !!n.document || !n.changeControlled || n.additionalProperties || n.validators.length > 0 || !!n.editor}>
  <summary>
    Lifecycle, documents, node validators &amp; editor
    {#if n.lifecycle}<span class="tag">lifecycle: {n.lifecycle}</span>{:else if inherited}<span class="tag muted">inherits {inherited.lifecycle} from {inherited.type}</span>{/if}
    {#if n.document}<span class="tag">document</span>{/if}
    {#if !n.changeControlled}<span class="tag muted">direct writes</span>{/if}
    {#if n.additionalProperties}<span class="tag muted">free-form</span>{/if}
    {#if n.validators.length}<span class="tag">{n.validators.length} node validator{n.validators.length > 1 ? 's' : ''}</span>{/if}
    {#if n.editor}<span class="tag">editor: {n.editor}</span>{/if}
  </summary>
  <div class="body" data-path="{path}.lifecycle">
    <div class="line">
      <label for="{path}-lc">Lifecycle</label>
      <select id="{path}-lc" bind:value={n.lifecycle} disabled={readonly}>
        <option value="">{inherited ? `— inherit (${inherited.lifecycle}) —` : '— none —'}</option>
        {#if n.lifecycle && !lifecycles.includes(n.lifecycle)}<option value={n.lifecycle}>{n.lifecycle} (unknown)</option>{/if}
        {#each lifecycles as l (l)}<option value={l}>{l}</option>{/each}
      </select>
      {#if (n.lifecycle || inherited) && onopenLifecycle}
        {@const target = n.lifecycle || inherited?.lifecycle || ''}
        <button type="button" class="small link" title="Open the lifecycle {target}" onclick={() => onopenLifecycle(target)}>Open {target} ↗</button>
      {/if}
      <label class="check" title="Off: the nodes are written directly, outside changes (no lifecycle)"><input type="checkbox" bind:checked={n.changeControlled} disabled={readonly || !!n.lifecycle} /> modified through changes only</label>
      <label class="check" title="Off (the default): a node carries the attributes of its type, and of its supertypes, only. On: it may carry any other property too"><input type="checkbox" bind:checked={n.additionalProperties} disabled={readonly} /> free-form: accepts properties that are no attribute</label>
    </div>
    <div class="field">
      <label for="{path}-doc">Embedded node types <span class="hint">(a document: comma-separated, attached by outgoing “contains” links)</span></label>
      <input id="{path}-doc" type="text" class="mono" bind:value={n.document} placeholder="Requirement, Chapter" list="{path}-types" disabled={readonly} />
      <datalist id="{path}-types">{#each typeNames as t (t)}<option value={t}></option>{/each}</datalist>
    </div>
    <div class="field" data-path="{path}.editor">
      <label for="{path}-editor">Editor <span class="hint">(the nodes open in this editor of the interface; empty: inherited from the parent type, else the default node editor)</span></label>
      <input id="{path}-editor" type="text" class="mono" class:bad={bad(`${path}.editor`)} bind:value={n.editor} placeholder="default node editor" list="{path}-editors" disabled={readonly} />
      <datalist id="{path}-editors">{#each nodeEditorNames() as e (e)}<option value={e}></option>{/each}</datalist>
    </div>
    <div class="validators" data-path="{path}.validators">
      <span class="label">Node validators <span class="hint">(algorithms checking the node as a whole, run in this order on create / update, after the attribute validators and those of the supertypes)</span></span>
      {#each n.validators as v, k}
        <div class="vrow" data-path="{path}.validators[{k}]">
          <select aria-label="Node validator instance" bind:value={n.validators[k]} class:bad={bad(`${path}.validators[${k}]`)} disabled={readonly}>
            <option value="">— validator —</option>
            {#if v && !validatorInstances.includes(v)}<option value={v}>{v} (unknown)</option>{/if}
            {#each validatorInstances as i (i)}<option value={i}>{i}</option>{/each}
          </select>
          {#if !readonly}
            <RowTools index={k} count={n.validators.length} label="the validator" onmove={(delta) => moveItem(n.validators, k, delta)} onremove={() => n.validators.splice(k, 1)} />
          {/if}
        </div>
      {/each}
      {#if !readonly}
        <button type="button" class="small" disabled={!validatorInstances.length} title={validatorInstances.length ? '' : 'Create a node_validator instance first (Algorithms)'} onclick={() => n.validators.push('')}>+ Node validator</button>
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
