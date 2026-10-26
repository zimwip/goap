<script lang="ts">
  // What a node type says beyond its attributes: its lifecycle (by name), the
  // node types it embeds when it is a document, whether it is change controlled,
  // the editor the user interface opens its nodes with, and the icon and color it draws them with.
  import type { NodeTypeForm } from '../methodologyForm';
  import { VISIBILITIES } from '../visibility';
  import { nodeEditorNames } from '../shell/registry';
  import Icon, { iconNames, type IconName } from '../shell/Icon.svelte';

  const COLOR = /^#[0-9a-fA-F]{6}$/;

  let {
    n = $bindable(),
    lifecycles,
    typeNames,
    inherited,
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
    /** opens the editor of a lifecycle, by name */
    onopenLifecycle?: (name: string) => void;
    /** does an issue exist at this path? */
    bad?: (path: string) => boolean;
    readonly?: boolean;
    path: string;
  } = $props();
</script>

<details class="meta" open={!!n.lifecycle || !!n.document || !n.changeControlled || n.additionalProperties || !!n.visibility || !!n.editor || !!n.icon || !!n.color}>
  <summary>
    Lifecycle, documents &amp; editor
    {#if n.lifecycle}<span class="tag">lifecycle: {n.lifecycle}</span>{:else if inherited}<span class="tag muted">inherits {inherited.lifecycle} from {inherited.type}</span>{/if}
    {#if n.document}<span class="tag">document</span>{/if}
    {#if !n.changeControlled}<span class="tag muted">direct writes</span>{/if}
    {#if n.additionalProperties}<span class="tag muted">free-form</span>{/if}
    {#if n.visibility}<span class="tag">visibility: {n.visibility}</span>{/if}
    {#if n.editor}<span class="tag">editor: {n.editor}</span>{/if}
    {#if n.icon || n.color}<span class="tag look" style:color={COLOR.test(n.color) ? n.color : undefined}><Icon name={(iconNames.includes(n.icon as IconName) ? n.icon : 'node') as IconName} size={12} /></span>{/if}
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
    <div class="line" data-path="{path}.visibility">
      <label for="{path}-vis">Visibility</label>
      <select id="{path}-vis" bind:value={n.visibility} disabled={readonly} title="How far a version is seen when its lifecycle state declares none (ADR 0161): personal its creator, private its responsible unit, project the project, tree the projects around it, public everyone">
        <option value="">— inherited, else project —</option>
        {#each VISIBILITIES as v (v)}<option value={v}>{v}</option>{/each}
      </select>
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
    <div class="line" data-path="{path}.icon">
      <label for="{path}-icon">Icon</label>
      <select id="{path}-icon" class:bad={bad(`${path}.icon`)} bind:value={n.icon} disabled={readonly}>
        <option value="">— inherited, else the default —</option>
        {#if n.icon && !iconNames.includes(n.icon as IconName)}<option value={n.icon}>{n.icon} (unknown)</option>{/if}
        {#each iconNames as i (i)}<option value={i}>{i}</option>{/each}
      </select>
      <span class="preview" style:color={COLOR.test(n.color) ? n.color : undefined} title="How the nodes are drawn"><Icon name={(iconNames.includes(n.icon as IconName) ? n.icon : 'node') as IconName} size={18} /></span>
      <label for="{path}-color" data-path="{path}.color">Color</label>
      <input id="{path}-color" type="text" class="mono color" class:bad={bad(`${path}.color`)} bind:value={n.color} placeholder="#rrggbb (inherited)" disabled={readonly} />
      <input type="color" aria-label="Pick the color" value={COLOR.test(n.color) ? n.color : '#888888'} oninput={(e) => (n.color = e.currentTarget.value)} disabled={readonly} />
      {#if n.color}<button type="button" class="small ghost" onclick={() => (n.color = '')} disabled={readonly}>Clear</button>{/if}
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
  .tag.look {
    display: inline-flex;
    vertical-align: middle;
    padding: 0.1rem 0.3rem;
  }
  .preview {
    display: inline-flex;
    color: var(--muted);
  }
  input.color {
    width: 8rem;
  }
  input[type='color'] {
    width: 2rem;
    padding: 0;
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
