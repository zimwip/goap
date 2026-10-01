<script lang="ts">
  // A process (or a method composing its own steps) as a flow of steps and conditions (xyflow + dagre), one level at a
  // time: the system of interest is a parent and its direct steps. A step is a node whose ports are its entry
  // conditions (left) and exit criteria (right); a link joins the exit criterion of a step to the entry condition it
  // meets in another. A step with sub-steps is a node too: zoom into it (double-click, or its button) to change the
  // system of interest and see its own flow; when it is not resolved inside, it is flagged here, because the level
  // cannot run through it until it is reworked. Dynamic conditions nothing establishes are read from a "Change state"
  // block, the inputs of the parent come from an "Inputs" block, what cannot be resolved ends in the orange blocks.
  // A step naming a capability is a boundary: the methods specializing it add variability and cut the traceability, so
  // they are only listed on it and drawn in a graph of their own. Click a condition to trace it: the links that carry
  // it are drawn on, the rest is dimmed. Right-click a step to open it.
  import { SvelteFlow, Background, Controls, MiniMap, MarkerType, type Node, type Edge, type NodeTypes } from '@xyflow/svelte';
  import '@xyflow/svelte/dist/style.css';
  import StepNode, { type StepNodeData } from './StepNode.svelte';
  import { layered, nodeHeight, NODE_W } from '../flowLayout';
  import type { LevelCheck, PlanPreview, ProcessGraph } from '../api';

  let {
    graph,
    plan,
    onstep,
    onmethod,
    levels = [],
    conditions,
    focus = '',
    at = '',
    only = '',
  }: {
    graph: ProcessGraph;
    plan?: PlanPreview;
    /** open the step of this path ("<process>/<step>/<sub-step>") */
    onstep?: (path: string) => void;
    /** open the graph of a method specializing a capability step: it is apart from this one */
    onmethod?: (name: string) => void;
    /** the levels of the process (CheckLevels): the steps and links of each, and what is not resolved */
    levels?: LevelCheck[];
    /** the dynamic conditions of the methodology (CEL over the state of the change); default: every name is declared */
    conditions?: { name: string; expr?: string }[];
    /** a step (path) to show selected */
    focus?: string;
    /** the system of interest the flow opens on (a parent path); default: the level of `focus`, else the root */
    at?: string;
    /** show this one step alone (its path), with what it takes and gives, instead of its whole level */
    only?: string;
  } = $props();

  const STATE = '__state';
  const INPUTS = '__inputs';
  const OUTPUTS = '__outputs';
  const UNRESOLVED = '__unresolved';
  const UNREACHED = '__unreached';
  const BOUNDARY = [STATE, INPUTS, OUTPUTS, UNRESOLVED, UNREACHED];
  const PALETTE = ['#4c78a8', '#f58518', '#54a24b', '#b279a2', '#e45756', '#72b7b2', '#eeca3b', '#9d755d'];
  const nodeTypes: NodeTypes = { step: StepNode as unknown as NodeTypes[string] };

  let traced = $state('');
  let nodes = $state.raw<Node[]>([]);
  let edges = $state.raw<Edge[]>([]);
  let moved = new Map<string, { x: number; y: number }>();
  let layoutKey = '';

  const colorMode = $derived(
    document.documentElement.dataset.theme === 'light' ? 'light' : document.documentElement.dataset.theme === 'dark' ? 'dark' : 'system',
  );

  // the system of interest: the path of a parent ("<process>", "<process>/<step>"...)
  const rootPath = $derived(graph.process ?? '');
  const parentOf = (p: string) => p.slice(0, Math.max(0, p.lastIndexOf('/')));
  let path = $state('');
  let pathFor = '';
  $effect(() => {
    const want = `${rootPath}|${at}`;
    if (pathFor !== want) {
      pathFor = want;
      path = at || (focus ? parentOf(focus) || rootPath : rootPath);
    }
  });
  let solo = $state('');
  let soloFor = '';
  $effect(() => {
    const want = `${rootPath}|${at}|${only}`;
    if (soloFor !== want) {
      soloFor = want;
      solo = only;
    }
  });
  const whole = $derived(levels.find((l) => l.path === path) ?? levels.find((l) => l.path === parentOf(path)) ?? levels.find((l) => l.path === rootPath));
  // alone: the step with its own inputs and outputs, as if the level were just it
  const level = $derived.by(() => {
    const n = solo ? whole?.steps?.find((x) => x.path === solo) : undefined;
    if (!whole || !n) return whole;
    return { ...whole, steps: [n], edges: [], inputs: {}, outputs: n.exit, order: [], gaps: (whole.gaps ?? []).filter((g) => g.step === n.name && g.kind === 'noop') };
  });
  const crumbs = $derived(
    (level?.path ?? rootPath)
      .split('/')
      .map((name, i, all) => ({ name, path: all.slice(0, i + 1).join('/') })),
  );
  const phases = $derived((levels.find((l) => l.path === rootPath)?.steps ?? []).map((n) => n.name ?? ''));
  const phaseColour = (p: string) => PALETTE[Math.max(0, phases.indexOf(p.split('/')[1] ?? '')) % PALETTE.length];
  const planned = $derived((plan?.actions ?? []).map((a) => a.step ?? ''));

  function build() {
    const l = level;
    const steps = l?.steps ?? [];
    const produced = new Set(steps.flatMap((n) => Object.keys(n.exit ?? {})));
    const inputs = l?.inputs ?? {};
    // what nothing at the level makes true and the parent does not give comes from the state of the change when it is
    // a dynamic condition (evaluated over it), else it is missing
    const exprOf = new Map((conditions ?? []).map((x) => [x.name, x.expr ?? '']));
    const dynamic = (k: string) => !conditions || exprOf.has(k);
    const foreign = (k: string) => !produced.has(k) && !(k in inputs);
    const external = [...new Set(steps.flatMap((n) => Object.keys(n.entry ?? {})).filter((k) => foreign(k) && dynamic(k)))].sort();
    const given = [...new Set(steps.flatMap((n) => Object.keys(n.entry ?? {})).filter((k) => k in inputs && !produced.has(k)))].sort();
    // consistency of the level: entry conditions a step can never get, outputs the steps never reach
    const missingOf = new Map<string, Set<string>>();
    const add = (path: string, k: string) => missingOf.set(path, (missingOf.get(path) ?? new Set()).add(k));
    const unreached = new Set<string>();
    for (const g of l?.gaps ?? []) {
      if (g.kind === 'blocked') {
        const n = steps.find((x) => x.name === g.step);
        for (const m of g.missing ?? []) if (n && m in (n.entry ?? {}) && (produced.has(m) || !dynamic(m))) add(n.path ?? '', m);
      } else if (g.kind === 'output') for (const m of g.missing ?? []) unreached.add(m);
    }
    for (const n of steps) for (const k of Object.keys(n.entry ?? {})) if (foreign(k) && !dynamic(k)) add(n.path ?? '', k);
    // an input that is also an output does nothing: drawn red on the step, or on the boxes for the parent itself
    const noopOf = new Map<string, Set<string>>();
    const parentNoop = new Set<string>();
    for (const g of l?.gaps ?? []) {
      if (g.kind !== 'noop') continue;
      if (!g.step) (g.missing ?? []).forEach((k) => parentNoop.add(k));
      else noopOf.set(g.step, new Set([...(noopOf.get(g.step) ?? []), ...(g.missing ?? [])]));
    }
    const unresolved = [...new Set([...missingOf.values()].flatMap((x) => [...x]))].sort();

    const c = traced;
    const ports = (n: { entry?: Record<string, boolean>; exit?: Record<string, boolean> }): Record<string, string> => {
      const out: Record<string, string> = {};
      if (!c) return out;
      if (n.entry && c in n.entry) out[c] = 'down';
      if (n.exit && c in n.exit) out[c] = out[c] ? 'focus' : 'up';
      return out;
    };
    const roleOf = (p: Record<string, string>) => (!c ? '' : Object.keys(p).length ? (Object.values(p).includes('focus') ? 'both' : p[c] === 'up' ? 'up' : 'down') : 'dim');
    const allOut = (ks: string[]) => ks.map((k) => [k, true] as [string, boolean]);

    const ns: Node[] = steps.map((n) => {
      const fp = ports(n);
      const p = n.path ?? '';
      const data: StepNodeData = {
        name: n.name ?? '',
        method: n.method ?? '',
        inputs: Object.entries(n.entry ?? {}),
        outputs: Object.entries(n.exit ?? {}),
        colour: phaseColour(p),
        kind: 'step',
        flowRole: roleOf(fp),
        flowPorts: fp,
        planned: planned.some((a) => a === p || a.startsWith(`${p}/`)),
        missing: [...(missingOf.get(p) ?? [])],
        bad: [...(noopOf.get(n.name ?? '') ?? [])],
        composite: !!n.composite,
        broken: !!n.broken,
        subSteps: n.subSteps ?? 0,
        foreach: n.foreach,
        groupBy: n.groupBy,
        onzoom: () => (path = p),
        ontrace: trace,
        variants: n.method === 'method' ? (graph.methods ?? []).filter((m) => m.for === n.capability).map((m) => m.name ?? '') : undefined,
        onvariant: onmethod,
      };
      return { id: p, type: 'step', position: { x: 0, y: 0 }, data, selected: !!focus && (p === focus || focus.startsWith(`${p}/`)) };
    });
    const boundary = (id: string, name: string, kind: StepNodeData['kind'], colour: string, io: { inputs?: string[]; outputs?: string[]; inputsKV?: [string, boolean][]; hints?: Record<string, string>; bad?: string[] }) => {
      const all = [...(io.inputs ?? io.inputsKV?.map(([k]) => k) ?? []), ...(io.outputs ?? [])];
      const fp: Record<string, string> = c && all.includes(c) ? { [c]: io.outputs ? 'up' : 'down' } : {};
      ns.push({
        id,
        type: 'step',
        position: { x: 0, y: 0 },
        selectable: false,
        data: { name, method: '', inputs: io.inputsKV ?? allOut(io.inputs ?? []), outputs: allOut(io.outputs ?? []), hints: io.hints, bad: io.bad, colour, kind, flowRole: c && !all.includes(c) ? 'dim' : roleOf(fp), flowPorts: fp, ontrace: trace } satisfies StepNodeData,
      });
    };
    if (given.length || parentNoop.size) boundary(INPUTS, l?.kind === 'step' ? `Inputs of ${(l.path ?? '').split('/').pop()}` : 'Inputs', 'inputs', 'var(--accent)', { outputs: [...new Set([...given, ...[...parentNoop].filter((k) => k in inputs)])].sort(), bad: [...parentNoop] });
    if (external.length) boundary(STATE, 'Change state', 'state', 'var(--info)', { outputs: external, hints: Object.fromEntries(external.map((k) => [k, exprOf.get(k) ?? ''])) });
    if (unresolved.length) boundary(UNRESOLVED, 'Unresolved inputs', 'unresolved', 'var(--warn)', { outputs: unresolved });
    // what the parent has to make true: the outputs its steps reach (the others end in the orange block)
    const reached = Object.entries(l?.outputs ?? {}).filter(([k]) => !unreached.has(k)).sort(([a], [b]) => a.localeCompare(b));
    if (reached.length) boundary(OUTPUTS, l?.kind === 'step' || l?.kind === 'agent' ? `Outputs of ${(l.path ?? '').split('/').pop()}` : 'Outputs', 'outputs', 'var(--ok)', { inputsKV: reached, bad: [...parentNoop] });
    if (unreached.size) boundary(UNREACHED, 'Unreached outputs', 'unreached', 'var(--warn)', { inputs: [...unreached].sort() });

    const es: Edge[] = [];
    const cls = (cond: string) => (!c ? 'link' : cond === c ? 'link flow-up' : 'link dim');
    for (const e of l?.edges ?? []) {
      for (const cond of e.conditions ?? []) {
        es.push({ id: `${e.from}:${cond}->${e.to}`, source: e.from ?? '', sourceHandle: `o:${cond}`, target: e.to ?? '', targetHandle: `i:${cond}`, class: cls(cond), data: { c: cond }, markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14 } });
      }
    }
    for (const n of steps) {
      for (const cond of Object.keys(n.entry ?? {})) {
        const from = cond in inputs && !produced.has(cond) ? INPUTS : !produced.has(cond) && dynamic(cond) ? STATE : '';
        if (from) es.push({ id: `${from}:${cond}->${n.path}`, source: from, sourceHandle: `o:${cond}`, target: n.path ?? '', targetHandle: `i:${cond}`, class: `${cls(cond)} external`, data: { c: cond } });
      }
    }
    for (const [p, set] of missingOf) {
      for (const m of set) es.push({ id: `miss:${m}->${p}`, source: UNRESOLVED, sourceHandle: `o:${m}`, target: p, targetHandle: `i:${m}`, class: 'missing', data: { c: m } });
    }
    for (const [k, v] of reached) {
      for (const n of steps) if (n.exit && n.exit[k] === v) es.push({ id: `out:${n.path}:${k}`, source: n.path ?? '', sourceHandle: `o:${k}`, target: OUTPUTS, targetHandle: `i:${k}`, class: cls(k), data: { c: k }, markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14 } });
    }
    for (const m of unreached) {
      for (const n of steps) if (n.exit && m in n.exit) es.push({ id: `unreached:${n.path}:${m}`, source: n.path ?? '', sourceHandle: `o:${m}`, target: UNREACHED, targetHandle: `i:${m}`, class: 'missing', data: { c: m } });
    }
    // a new level (not a trace) is laid out again; a dragged node keeps its place
    const key = JSON.stringify([l?.path, ns.map((n) => n.id), es.map((e) => e.id).sort(), ns.map((n) => (n.data as StepNodeData).inputs.length + ':' + (n.data as StepNodeData).outputs.length)]);
    if (key !== layoutKey) {
      layoutKey = key;
      moved = new Map();
    }
    place(ns, es);
    nodes = ns;
    edges = es;
  }

  function place(ns: Node[], es: Edge[]) {
    const pos = layered(
      ns.map((n) => ({ id: n.id, h: nodeHeight(Math.max((n.data as StepNodeData).inputs.length, (n.data as StepNodeData).outputs.length) + ((n.data as StepNodeData).composite ? 1 : 0) + ((n.data as StepNodeData).foreach ? 1 : 0)), w: NODE_W })),
      es,
    );
    for (const n of ns) n.position = moved.get(n.id) ?? pos.get(n.id) ?? { x: 0, y: 0 };
  }

  function trace(cond: string) {
    traced = traced === cond ? '' : cond;
  }

  $effect(() => {
    void graph;
    void plan;
    void traced;
    void levels;
    void conditions;
    void focus;
    void path;
    void solo;
    build();
  });

  // full screen (Esc leaves it); the canvas follows the browser's state, which can also change on its own
  let box = $state<HTMLElement>();
  let full = $state(false);
  function toggleFull() {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void box?.requestFullscreen?.();
  }

  function relayout() {
    moved = new Map();
    build();
  }
</script>

<svelte:document onfullscreenchange={() => (full = document.fullscreenElement === box)} />

<div class="canvas" class:full bind:this={box}>
  <SvelteFlow
    bind:nodes
    bind:edges
    {nodeTypes}
    {colorMode}
    fitView
    fitViewOptions={{ padding: 0.15, maxZoom: 1 }}
    nodesConnectable={false}
    deleteKey={null}
    minZoom={0.2}
    onedgeclick={({ edge }) => trace(String((edge.data as { c?: string } | undefined)?.c ?? ''))}
    onpaneclick={() => (traced = '')}
    onnodedragstop={({ targetNode }) => targetNode && moved.set(targetNode.id, { ...targetNode.position })}
    onnodecontextmenu={({ node, event }) => {
      event.preventDefault();
      if (BOUNDARY.includes(node.id)) return;
      const d = node.data as StepNodeData;
      if (d.method === 'variant') onmethod?.(d.name);
      else onstep?.(node.id);
    }}
    proOptions={{ hideAttribution: true }}
  >
    <Background gap={20} />
    <Controls showLock={false} />
    <MiniMap pannable zoomable height={90} width={140} />
  </SvelteFlow>
  <button type="button" class="fs" onclick={toggleFull} aria-pressed={full} aria-label={full ? 'Exit full screen' : 'Full screen'} title={full ? 'Exit full screen (Esc)' : 'Full screen'}>
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      {#if full}
        <path d="M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5" />
      {:else}
        <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />
      {/if}
    </svg>
  </button>
  <div class="overlay">
    {#if solo}<button type="button" class="small" onclick={() => (solo = '')} title="Show the whole level this step belongs to">Show its level</button>{/if}
    <nav class="crumbs" aria-label="Level">
      {#each crumbs as cr, i (cr.path)}
        {#if i}<span class="sep">›</span>{/if}
        <button type="button" class="small" class:primary={i === crumbs.length - 1} disabled={i === crumbs.length - 1} onclick={() => ((solo = ''), (path = cr.path))}>{cr.name}</button>
      {/each}
    </nav>
    {#if level?.agent}
      <span class="legend small" title="The agent plans the actions in the order that reaches the goal">agent <b class="mono">{level.agent}</b>{#if level.goal} → goal <b class="mono">{level.goal}</b>{/if}</span>
    {/if}
    <button type="button" class="small" onclick={relayout} title="Arrange the steps automatically">Auto layout</button>
    <span class="legend small">
      {#if traced}
        <b class="mono">{traced}</b> <i class="l up"></i>carries it
        <button type="button" class="ghost small" onclick={() => (traced = '')}>clear</button>
      {:else}
        click a condition to trace it · right-click a step to open it · double-click a step with sub-steps to zoom into it · green block: what the level makes true · blue block: dynamic conditions read from the state of the change · orange: unresolved · red: an input that is also an output
      {/if}
    </span>
  </div>
</div>

<style>
  .canvas {
    position: relative;
    height: min(70vh, 640px);
    min-height: 320px;
  }
  .fs {
    position: absolute;
    top: 10px;
    right: 10px;
    z-index: 6;
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    padding: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    cursor: pointer;
  }
  .fs:hover {
    background: var(--hover);
  }
  .crumbs {
    display: flex;
    align-items: center;
    gap: 2px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 1px 4px;
  }
  .sep {
    color: var(--muted);
  }
  .canvas.full {
    height: 100vh;
    background: var(--bg);
  }
  .overlay {
    position: absolute;
    top: 10px;
    left: 10px;
    /* leave the full screen icon (top right) uncovered: the legend wraps instead of running under it */
    right: 52px;
    z-index: 5;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    pointer-events: none;
  }
  .overlay > * {
    pointer-events: auto;
  }
  .legend {
    display: flex;
    gap: 6px;
    align-items: center;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 2px 8px;
    color: var(--muted);
  }
  .l {
    display: inline-block;
    width: 18px;
    border-top: 3px solid var(--info);
  }
  .canvas :global(.svelte-flow) {
    --xy-background-color: var(--bg);
    --xy-node-background-color: var(--surface);
    --xy-minimap-background-color: var(--surface);
    --xy-controls-button-background-color: var(--surface);
    --xy-controls-button-background-color-hover: var(--hover);
    --xy-controls-button-color: var(--text);
    --xy-controls-button-border-color: var(--border);
  }
  .canvas :global(.svelte-flow__edge.link path) {
    stroke: var(--muted);
    stroke-width: 1.5;
  }
  .canvas :global(.svelte-flow__edge.missing path) {
    stroke: var(--warn);
    stroke-width: 2;
    stroke-dasharray: 6 4;
  }
  .canvas :global(.svelte-flow__edge.external path) {
    stroke-dasharray: 2 3;
    stroke-width: 1;
  }
  .canvas :global(.svelte-flow__edge.flow-up path) {
    stroke: var(--info) !important;
    stroke-width: 3 !important;
    stroke-dasharray: none !important;
  }
  .canvas :global(.svelte-flow__edge.dim) {
    opacity: 0.12;
  }
  .canvas :global(.svelte-flow__edge) {
    cursor: pointer;
  }
</style>
