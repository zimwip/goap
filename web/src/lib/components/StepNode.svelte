<script lang="ts" module>
  export interface StepNodeData {
    [key: string]: unknown;
    name: string;
    /** how the step is done (action, agent, process, method, manual) */
    method: string;
    /** the condition and the value its step needs / makes true */
    inputs: [string, boolean][];
    outputs: [string, boolean][];
    colour: string;
    kind: 'step' | 'inputs' | 'outputs' | 'state' | 'unresolved' | 'unreached';
    /** entry conditions nothing at the level of the step makes true: drawn as unresolved */
    missing?: string[];
    /** conditions that are both an input and an output of the same activity: nothing is done about them */
    bad?: string[];
    /** the step has sub-steps: zoom into its own level */
    composite?: boolean;
    subSteps?: number;
    /** the step runs once per element (or group) of this CEL list, in parallel streams (ADR 0050) */
    foreach?: string;
    groupBy?: string;
    /** that level, or one below it, is not resolved: this level cannot run through the step */
    broken?: boolean;
    onzoom?: () => void;
    /** tooltip of a port: the expression of a dynamic condition */
    hints?: Record<string, string>;
    /** '', 'dim', 'up' (feeds the traced condition), 'down' (needs it), 'both' */
    flowRole: string;
    /** port → 'focus' | 'up' | 'down' while a condition is traced */
    flowPorts: Record<string, string>;
    /** in the plan the process's agent reaches */
    planned?: boolean;
    ontrace?: (condition: string) => void;
    /** a step naming a capability: the methods that specialize it, each with a graph of its own */
    variants?: string[];
    onvariant?: (method: string) => void;
  }
</script>

<script lang="ts">
  import { Handle, Position, useUpdateNodeInternals } from '@xyflow/svelte';

  let { id, data, selected }: { id: string; data: StepNodeData; selected?: boolean } = $props();

  const update = useUpdateNodeInternals();
  $effect(() => {
    JSON.stringify([data.inputs, data.outputs]);
    update(id);
  });
  const label = (c: string, v: boolean) => (v ? c : `¬ ${c}`);
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  role="presentation"
  ondblclick={() => data.composite && data.onzoom?.()}
  class="node"
  class:selected
  class:pre={data.kind === 'state' || data.kind === 'inputs'}
  class:out={data.kind === 'outputs'}
  class:broken={data.broken}
  class:red={!!data.bad?.length}
  class:gap={data.kind === 'unresolved' || data.kind === 'unreached'}
  class:planned={data.planned}
  class:dim={data.flowRole === 'dim'}
  class:f-up={data.flowRole === 'up' || data.flowRole === 'both'}
  class:f-down={data.flowRole === 'down'}
  style:--phase={data.colour}
>
  <div class="head">
    <span class="title">{data.name}</span>
    {#if data.kind === 'step'}<span class="kind">{data.method}</span>{/if}
  </div>
  {#if data.method === 'variant'}
    <div class="zoom">
      <button type="button" class="chip" title="Open the method: its steps are drawn in a graph of their own" onclick={(e) => (e.stopPropagation(), data.onvariant?.(data.name))}>open method</button>
      <span class="hint">{data.target}</span>
    </div>
  {/if}
  {#if data.foreach}
    <div class="zoom">
      <span class="chip" title="One parallel stream per element, each with the most specific method for it: {data.foreach}">∀ {data.foreach}</span>
      {#if data.groupBy}<span class="hint" title="One stream per group: {data.groupBy}">by {data.groupBy}</span>{/if}
    </div>
  {/if}
  {#if data.composite}
    <div class="zoom">
      <button type="button" class="chip" title="Zoom in: the level below is another system of interest" onclick={(e) => (e.stopPropagation(), data.onzoom?.())}>⤢ {data.subSteps} {data.method === 'action' || data.method === 'agent' ? 'action' : data.method === 'method' ? 'method' : 'sub-step'}{data.subSteps === 1 ? '' : 's'}</button>
      {#if data.broken}<span class="incomplete" title="Not resolved inside: rework it, the process cannot run through it">⚠ incomplete</span>{/if}
    </div>
  {/if}
  {#if data.variants?.length}
    <div class="variants" title="Specialized by methods: their steps are not part of this graph">
      {#each data.variants as m (m)}
        <button type="button" class="chip" onclick={(e) => (e.stopPropagation(), data.onvariant?.(m))}>{m}</button>
      {/each}
    </div>
  {/if}
  <div class="ports">
    <div class="col-in">
      {#each data.inputs as [c, v] (c)}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
        <div class="port in {data.flowPorts[c] ? `p-${data.flowPorts[c]}` : ''}"
          class:p-missing={data.missing?.includes(c) || data.kind === 'unreached'}
          class:p-bad={data.bad?.includes(c)} title="{label(c, v)} (click to trace)" onclick={(e) => (e.stopPropagation(), data.ontrace?.(c))}>
          <Handle type="target" position={Position.Left} id={`i:${c}`} isConnectable={false} />
          <span>{label(c, v)}</span>
        </div>
      {/each}
    </div>
    <div class="col-out">
      {#each data.outputs as [c, v] (c)}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
        <div class="port out {data.flowPorts[c] ? `p-${data.flowPorts[c]}` : ''}" class:p-bad={data.bad?.includes(c)} title="{label(c, v)}{data.hints?.[c] ? `: ${data.hints[c]}` : ''} (click to trace)" onclick={(e) => (e.stopPropagation(), data.ontrace?.(c))}>
          <span>{label(c, v)}</span>
          <Handle type="source" position={Position.Right} id={`o:${c}`} isConnectable={false} />
        </div>
      {/each}
    </div>
  </div>
</div>

<style>
  .node {
    min-width: 180px;
    max-width: 260px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-top: 3px solid var(--phase);
    border-radius: 8px;
    box-shadow: var(--shadow);
    font-size: 12px;
    color: var(--text);
  }
  .node.selected {
    outline: 2px solid var(--accent);
  }
  .node.planned {
    border-color: var(--ok);
  }
  .node.pre {
    background: var(--info-soft);
    border-color: var(--info);
    border-style: dashed;
    min-width: 120px;
  }
  .node.out {
    background: var(--ok-soft);
    border: 1px dashed var(--ok);
    min-width: 120px;
  }
  .node.gap {
    background: var(--warn-soft);
    border: 1px dashed var(--warn);
    min-width: 120px;
  }
  .node.gap .title {
    color: var(--warn);
  }
  .node.gap .port {
    color: var(--warn);
    font-weight: 600;
  }
  .port.p-missing {
    color: var(--warn);
    font-weight: 700;
    outline: 1px dashed var(--warn);
    background: var(--warn-soft);
  }
  .port.p-missing::before {
    content: '⚠ ';
  }
  .node.red {
    border-color: var(--danger);
  }
  .port.p-bad {
    color: var(--danger);
    font-weight: 700;
    outline: 1px solid var(--danger);
    background: var(--danger-soft);
  }
  .port.p-bad::before {
    content: '= ';
  }
  .node.dim {
    opacity: 0.35;
  }
  .node.f-up {
    border-color: var(--info);
  }
  .node.f-down {
    border-color: var(--warn);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
  }
  .title {
    flex: 1;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .kind {
    font-size: 9px;
    font-family: var(--mono);
    color: var(--muted);
    background: var(--surface-3);
    padding: 1px 4px;
    border-radius: 3px;
  }
  .zoom {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    padding: 4px 10px 0;
  }
  .incomplete {
    font-size: 10px;
    font-weight: 700;
    color: var(--warn);
  }
  .node.broken {
    border-color: var(--warn);
    border-style: dashed;
    background: var(--warn-soft);
  }
  .variants {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    padding: 4px 10px 0;
  }
  .chip {
    font-size: 10px;
    font-family: var(--mono);
    padding: 0 6px;
    border: 1px dashed var(--accent);
    border-radius: 9px;
    background: var(--accent-soft);
    color: var(--accent);
    cursor: pointer;
  }
  .ports {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    padding: 4px 0;
  }
  .col-in,
  .col-out {
    display: flex;
    flex-direction: column;
  }
  .col-out {
    align-items: flex-end;
  }
  .port {
    position: relative;
    padding: 2px 10px;
    font-family: var(--mono);
    font-size: 11px;
    color: var(--muted);
    cursor: pointer;
    border-radius: 3px;
  }
  .port:hover {
    background: var(--hover);
  }
  .port.p-up {
    color: var(--info);
    font-weight: 700;
  }
  .port.p-down {
    color: var(--warn);
    font-weight: 700;
  }
  .port.p-focus {
    background: var(--accent-soft);
    color: var(--accent);
    font-weight: 700;
  }
  .node :global(.svelte-flow__handle) {
    width: 7px;
    height: 7px;
    min-width: 0;
    min-height: 0;
    background: var(--muted);
    border: 1px solid var(--surface);
  }
</style>
