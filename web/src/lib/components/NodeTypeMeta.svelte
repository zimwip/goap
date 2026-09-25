<script lang="ts">
  // What a node type says beyond its properties: its lifecycle (by name), the
  // node types it embeds when it is a document, and whether it is change controlled.
  import type { NodeTypeForm } from '../methodologyForm';

  let {
    n = $bindable(),
    lifecycles,
    typeNames,
    inherited,
    readonly = false,
    path,
  }: {
    n: NodeTypeForm;
    lifecycles: string[];
    typeNames: string[];
    /** ancestor whose lifecycle applies when the type names none */
    inherited?: { type: string; lifecycle: string };
    readonly?: boolean;
    path: string;
  } = $props();
</script>

<details class="meta" open={!!n.lifecycle || !!n.document || !n.changeControlled}>
  <summary>
    Lifecycle &amp; documents
    {#if n.lifecycle}<span class="tag">lifecycle: {n.lifecycle}</span>{:else if inherited}<span class="tag muted">inherits {inherited.lifecycle} from {inherited.type}</span>{/if}
    {#if n.document}<span class="tag">document</span>{/if}
    {#if !n.changeControlled}<span class="tag muted">direct writes</span>{/if}
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
