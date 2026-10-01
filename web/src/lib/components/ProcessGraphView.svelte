<script lang="ts">
  // A process as a graph (ADR 0036 §4). The steps that run something are laid out in layers: an edge goes from a step
  // whose exit criteria meet what another step needs to be entered (the conditions sequence the steps, not their
  // position), implied edges left out; the colour is the top-level phase. A step naming a capability shows its methods;
  // selecting a method focuses on it: its context, guidance, references, roles, and the agent that acts with its actions.
  import type { GraphStep, MethodologyMethod, PlanPreview, ProcessGraph } from '../api';
  import { renderMarkdown } from '../markdown';

  let {
    graph,
    onprocess,
    plan,
    planIssue = '',
    overrides = {},
    setOverride,
    previewAgent,
  }: {
    graph: ProcessGraph;
    onprocess?: (name: string) => void;
    /** the plan the process's agent reaches from the conditions below, overridden on top of an empty blackboard */
    plan?: PlanPreview;
    planIssue?: string;
    overrides?: Record<string, boolean>;
    setOverride?: (name: string, value: boolean | undefined) => void;
    /**
     * Plans toward an agent/goal that is not the process itself - a method's actor, typically - with the same
     * condition overrides: this is where a hybrid or utility planner actually runs (the process's own agent is
     * always goap, ADR 0034).
     */
    previewAgent?: (agent: string, goal: string) => Promise<{ preview?: PlanPreview; issues?: { message?: string }[] }>;
  } = $props();

  let subPlan = $state<{ for: string; preview?: PlanPreview; issue?: string } | undefined>();
  let subPlanBusy = $state(false);
  async function runSubPreview(agent: string, goal: string) {
    subPlanBusy = true;
    try {
      const r = await previewAgent?.(agent, goal);
      subPlan = { for: agent, preview: r?.preview, issue: r?.issues?.length ? r.issues.map((i) => i.message).join('; ') : '' };
    } finally {
      subPlanBusy = false;
    }
  }

  const W = 210;
  const H = 58;
  const GX = 28;
  const GY = 44;
  const PAD = 24;
  const TOP = 16;
  const MH = 46;
  const MGAP = 16;

  const steps = $derived(graph.steps ?? []);
  const leaves = $derived(steps.filter((s) => s.leaf));
  const methodsOf = (cap: string | undefined): MethodologyMethod[] => (graph.methods ?? []).filter((m) => cap && m.for === cap);
  const heightOf = (s: GraphStep) => H + (s.method === 'method' ? MGAP + MH : 0);

  // every condition name a leaf step's entry or exit names: the world the process-level plan preview plans over
  const conditionNames = $derived([...new Set(leaves.flatMap((s) => [...Object.keys(s.entry ?? {}), ...Object.keys(s.exit ?? {})]))].sort());
  const inPlan = $derived(new Set((plan?.actions ?? []).map((a) => a.step ?? '')));
  const planIndex = $derived(new Map((plan?.actions ?? []).map((a, i) => [a.step ?? '', i])));

  // top-level phase of a step: its first segment after the process
  const phaseOf = (path: string) => path.split('/')[1] ?? '';
  const phases = $derived(steps.filter((s) => (s.depth ?? 0) === 0).map((s) => s.name ?? ''));
  const PALETTE = ['#4c78a8', '#f58518', '#54a24b', '#b279a2', '#e45756', '#72b7b2', '#eeca3b', '#9d755d', '#bab0ac', '#ff9da6'];
  const colour = (path: string) => PALETTE[Math.max(0, phases.indexOf(phaseOf(path))) % PALETTE.length];

  interface Box {
    step: GraphStep;
    x: number;
    y: number;
    h: number;
  }

  // layers by the longest path from the steps nothing leads to (back edges of cycles ignored), top to bottom
  const layout = $derived.by(() => {
    const order = new Map(leaves.map((s, i) => [s.path ?? '', i]));
    const out = new Map<string, string[]>();
    for (const e of graph.edges ?? []) {
      if (order.has(e.from ?? '') && order.has(e.to ?? '')) out.set(e.from ?? '', [...(out.get(e.from ?? '') ?? []), e.to ?? '']);
    }
    // drop back edges (DFS in declaration order)
    const state = new Map<string, number>();
    const preds = new Map<string, string[]>();
    const visit = (n: string) => {
      state.set(n, 1);
      for (const m of out.get(n) ?? []) {
        if (state.get(m) === 1) continue;
        preds.set(m, [...(preds.get(m) ?? []), n]);
        if (!state.get(m)) visit(m);
      }
      state.set(n, 2);
    };
    for (const s of leaves) if (!state.get(s.path ?? '')) visit(s.path ?? '');
    const layer = new Map<string, number>();
    const depthOf = (n: string): number => {
      if (layer.has(n)) return layer.get(n)!;
      layer.set(n, 0);
      const d = Math.max(0, ...(preds.get(n) ?? []).map((p) => depthOf(p) + 1));
      layer.set(n, d);
      return d;
    };
    const rows: GraphStep[][] = [];
    for (const s of leaves) (rows[depthOf(s.path ?? '')] ??= []).push(s);
    const boxes = new Map<string, Box>();
    let width = 0;
    let y = TOP;
    for (const row of rows) {
      if (!row) continue;
      row.sort((a, b) => (order.get(a.path ?? '') ?? 0) - (order.get(b.path ?? '') ?? 0));
      let x = PAD;
      let h = 0;
      for (const s of row) {
        boxes.set(s.path ?? '', { step: s, x, y, h: heightOf(s) });
        x += W + GX;
        h = Math.max(h, heightOf(s));
      }
      width = Math.max(width, x - GX + PAD);
      y += h + GY;
    }
    // centre each row
    for (const row of rows) {
      if (!row?.length) continue;
      const rw = row.length * (W + GX) - GX;
      const off = (width - PAD * 2 - rw) / 2;
      for (const s of row) boxes.get(s.path ?? '')!.x += off;
    }
    return { boxes, width, height: y - GY + PAD };
  });

  // fit the graph in the canvas when it is (re)built
  let cw = $state(800);
  let ch = $state(560);
  $effect(() => {
    const k = Math.min(1, cw / Math.max(1, layout.width), ch / Math.max(1, layout.height));
    scale = k;
    tx = (cw - layout.width * k) / 2;
    ty = 0;
  });

  type Focus = { kind: 'step'; path: string } | { kind: 'method'; name: string } | null;
  let focus = $state<Focus>(null);
  const focusPath = $derived(focus?.kind === 'step' ? focus.path : '');
  const focusName = $derived(focus?.kind === 'method' ? focus.name : '');
  const focusStep = $derived(focusPath ? steps.find((s) => s.path === focusPath) : undefined);
  const focusMethod = $derived(focusName ? (graph.methods ?? []).find((m) => m.name === focusName) : undefined);
  const agentOf = (name: string | undefined) => (graph.agents ?? []).find((a) => a.name === name);
  const related = $derived.by(() => {
    const p = focus?.kind === 'step' ? focus.path : '';
    return new Set((graph.edges ?? []).filter((e) => e.from === p || e.to === p).flatMap((e) => [e.from, e.to]));
  });

  // pan and zoom
  let scale = $state(1);
  let tx = $state(0);
  let ty = $state(0);
  const zoomBy = (k: number) => (scale = Math.min(3, Math.max(0.2, scale * k)));
  let drag: { x: number; y: number; tx: number; ty: number } | null = null;
  function wheel(e: WheelEvent) {
    e.preventDefault();
    const k = e.deltaY < 0 ? 1.1 : 1 / 1.1;
    zoomBy(k);
  }
  function down(e: PointerEvent) {
    if ((e.target as Element).closest('.node')) return;
    drag = { x: e.clientX, y: e.clientY, tx, ty };
    (e.currentTarget as Element).setPointerCapture(e.pointerId);
  }
  function move(e: PointerEvent) {
    if (!drag) return;
    tx = drag.tx + e.clientX - drag.x;
    ty = drag.ty + e.clientY - drag.y;
  }

  function edgePath(a: Box, b: Box): string {
    const x1 = a.x + W / 2;
    const y1 = a.y + a.h;
    const x2 = b.x + W / 2;
    const y2 = b.y;
    if (y2 > y1) {
      const my = (y1 + y2) / 2;
      return `M${x1},${y1} C${x1},${my} ${x2},${my} ${x2},${y2}`;
    }
    // same row or upwards (a cycle): loop around the right side
    const rx = Math.max(a.x, b.x) + W + 20;
    return `M${a.x + W},${a.y + a.h / 2} C${rx},${a.y + a.h / 2} ${rx},${b.y + b.h / 2} ${b.x + W},${b.y + b.h / 2}`;
  }
  const label = (s: GraphStep) => (s.method === 'manual' ? 'by hand' : `${s.method} ${s.target ?? ''}`);
  const conds = (m: Record<string, boolean> | undefined) =>
    Object.entries(m ?? {})
      .map(([k, v]) => (v ? k : `!${k}`))
      .sort();
</script>

<div class="pg">
  <div class="left">
  <div class="legend">
    {#each phases as ph (ph)}<span><i style="background: {colour(`x/${ph}`)}"></i>{ph}</span>{/each}
    <span class="hint">edges: the conditions that chain the steps · methods: the candidates below a capability step · wheel to zoom, drag to pan</span>
  </div>
  {#if conditionNames.length}
    <div class="conditions" aria-label="Condition overrides">
      <span class="hint">Conditions (click to force true/false, again for auto):</span>
      {#each conditionNames as name (name)}
        {@const v = overrides[name]}
        <button
          type="button"
          class="cond"
          class:t={v === true}
          class:f={v === false}
          title="{name}: {v === undefined ? 'auto — the planner evaluates it live' : v ? 'forced true' : 'forced false'}"
          onclick={() => setOverride?.(name, v === undefined ? true : v === true ? false : undefined)}
        >
          {name}{v === true ? ' = true' : v === false ? ' = false' : ''}
        </button>
      {/each}
    </div>
  {/if}
  <div class="plan-banner">
    {#if planIssue}
      <span class="hint">{planIssue}</span>
    {:else if plan?.reached}
      <span class="ok">✓ the goal already holds in this world</span>
    {:else if plan?.actions?.length}
      <span>
        <strong>{plan.planner}</strong> plans ({plan.actions.length} step(s), cost {plan.cost}):
        {plan.actions.map((a) => a.step?.split('/').pop()).join(' → ')}
      </span>
    {:else if plan?.awaiting?.length}
      <span class="hint">waiting on: {plan.awaiting.join(', ')} (established outside this process)</span>
    {:else if plan}
      <span class="hint">no plan reaches the goal from this world</span>
    {/if}
  </div>
  <div class="canvas" role="presentation" bind:clientWidth={cw} bind:clientHeight={ch} onwheel={wheel} onpointerdown={down} onpointermove={move} onpointerup={() => (drag = null)}>
    <svg width="100%" height="100%" aria-label="Process graph of {graph.process}">
      <defs>
        <marker id="pg-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" fill="currentColor" />
        </marker>
      </defs>
      <g transform="translate({tx},{ty}) scale({scale})">
        {#each graph.edges ?? [] as e (`${e.from}>${e.to}`)}
          {@const a = layout.boxes.get(e.from ?? '')}
          {@const b = layout.boxes.get(e.to ?? '')}
          {#if a && b}
            <path
              class="edge"
              class:hot={focus?.kind === 'step' && (focus.path === e.from || focus.path === e.to)}
              class:planned={inPlan.has(e.from ?? '') && inPlan.has(e.to ?? '')}
              d={edgePath(a, b)}
              marker-end="url(#pg-arrow)"
            >
              <title>{e.from} → {e.to}: {(e.conditions ?? []).join(', ')}</title>
            </path>
          {/if}
        {/each}
        {#each [...layout.boxes.values()] as b (b.step.path)}
          {@const s = b.step}
          <g
            class="node"
            class:sel={focus?.kind === 'step' && focus.path === s.path}
            class:dim={focus?.kind === 'step' && !related.has(s.path) && focus.path !== s.path}
            class:planned={inPlan.has(s.path ?? '')}
            transform="translate({b.x},{b.y})"
            role="button"
            tabindex="0"
            onclick={() => (focus = { kind: 'step', path: s.path ?? '' })}
            ondblclick={() => s.method === 'process' && s.process && !s.process.includes('/') && onprocess?.(s.process)}
            onkeydown={(ev) => ev.key === 'Enter' && (focus = { kind: 'step', path: s.path ?? '' })}
          >
            <rect width={W} height={H} rx="6" style="stroke: {colour(s.path ?? '')}" />
            <rect width="6" height={H} rx="3" style="fill: {colour(s.path ?? '')}" />
            {#if inPlan.has(s.path ?? '')}
              <circle cx={W - 14} cy="14" r="10" class="plan-badge" />
              <text x={W - 14} y="18" text-anchor="middle" class="plan-badge-text">{(planIndex.get(s.path ?? '') ?? 0) + 1}</text>
            {/if}
            <text x="14" y="18" class="name">{s.name}</text>
            <text x="14" y="34" class="sub">{label(s).slice(0, 34)}</text>
            <text x="14" y="49" class="sub">{s.roles?.responsible ? `R ${s.roles.responsible}` : ''}{s.roles?.accountable ? ` · A ${s.roles.accountable}` : ''}</text>
            {#if s.method === 'method'}
              {@const ms = methodsOf(s.capability)}
              {@const mw = Math.min(W, (W - 6 * Math.max(0, ms.length - 1)) / Math.max(1, ms.length))}
              <line x1={W / 2} y1={H} x2={W / 2} y2={H + MGAP} class="connector" marker-end="url(#pg-arrow)" />
              {#each ms as m, i (m.name)}
                <g
                  class="node method-box"
                  class:sel={focus?.kind === 'method' && focus.name === m.name}
                  transform="translate({i * (mw + 6)},{H + MGAP})"
                  role="button"
                  tabindex="0"
                  onclick={(ev) => (ev.stopPropagation(), (focus = { kind: 'method', name: m.name ?? '' }))}
                  onkeydown={(ev) => ev.key === 'Enter' && (focus = { kind: 'method', name: m.name ?? '' })}
                >
                  <rect width={mw} height={MH} rx="6" />
                  <text x="8" y="16" class="name">{(m.name ?? '').slice(0, 20)}</text>
                  <text x="8" y="30" class="sub">{m.when ? `when ${m.when}`.slice(0, 26) : 'always'}</text>
                  {#if m.priority}<text x={mw - 6} y="16" text-anchor="end" class="sub">p{m.priority}</text>{/if}
                </g>
              {/each}
            {/if}
          </g>
        {/each}
      </g>
    </svg>
  </div>
  </div>

  <aside class="focus" aria-label="Focus">
    {#if focusStep}
      <h4>{focusStep.name} <span class="hint mono">{focusStep.path}</span></h4>
      {#if focusStep.description}<p>{focusStep.description}</p>{/if}
      <p class="hint">{label(focusStep)}</p>
      {#if focusStep.roles?.responsible || focusStep.roles?.accountable}
        <p>R <strong>{focusStep.roles?.responsible || '—'}</strong> · A <strong>{focusStep.roles?.accountable || '—'}</strong>
          {#if focusStep.roles?.consulted?.length} · C {focusStep.roles.consulted.join(', ')}{/if}
          {#if focusStep.roles?.informed?.length} · I {focusStep.roles.informed.join(', ')}{/if}</p>
      {/if}
      <p><span class="k">Needs</span> {#each conds(focusStep.entry) as c (c)}<code>{c}</code> {:else}—{/each}</p>
      <p><span class="k">Done when</span> {#each conds(focusStep.exit) as c (c)}<code>{c}</code> {:else}—{/each}</p>
      {#if focusStep.guidance}<div class="md">{@html renderMarkdown(focusStep.guidance)}</div>{/if}
      {#each focusStep.references ?? [] as r (r.ref)}<p class="hint">📄 {r.title || r.ref} <code>{r.ref}</code>{r.section ? ` — ${r.section}` : ''}</p>{/each}
      {#if focusStep.method === 'method'}
        <p class="k">Methods</p>
        {#each methodsOf(focusStep.capability) as m (m.name)}
          <button type="button" class="link" onclick={() => (focus = { kind: 'method', name: m.name ?? '' })}>{m.name}</button>
          <span class="hint">{m.when ? `when ${m.when}` : 'always'}{m.priority ? ` · priority ${m.priority}` : ''}</span><br />
        {/each}
      {/if}
      {#if focusStep.method === 'process' && focusStep.process && !focusStep.process.includes('/')}
        <button type="button" class="small" onclick={() => onprocess?.(focusStep.process ?? '')}>Open the process {focusStep.process}</button>
      {/if}
    {:else if focusMethod}
      {@const ag = agentOf(focusMethod.agent)}
      <h4>Method {focusMethod.name}</h4>
      <p class="hint">provides <code>{focusMethod.for}</code> · {focusMethod.when ? 'when' : 'always'} {#if focusMethod.when}<code>{focusMethod.when}</code>{/if}{focusMethod.priority ? ` · priority ${focusMethod.priority}` : ''}</p>
      {#if focusMethod.description}<p>{focusMethod.description}</p>{/if}
      {#if focusMethod.guidance}<div class="md">{@html renderMarkdown(focusMethod.guidance)}</div>{/if}
      {#each focusMethod.references ?? [] as r (r.ref)}<p class="hint">📄 {r.title || r.ref} <code>{r.ref}</code>{r.section ? ` — ${r.section}` : ''}</p>{/each}
      {#if focusMethod.deliverables?.length}<p><span class="k">Produces</span> {focusMethod.deliverables.join(', ')}</p>{/if}
      {#if focusMethod.roles?.responsible || focusMethod.roles?.accountable}
        <p>R <strong>{focusMethod.roles?.responsible || '—'}</strong> · A <strong>{focusMethod.roles?.accountable || '—'}</strong></p>
      {/if}
      <p><span class="k">Actor</span> agent <strong>{focusMethod.agent}</strong> → goal <code>{graph.methodGoals?.[focusMethod.name ?? ''] ?? focusMethod.goal}</code>
        {#if ag}<span class="hint">({ag.planner})</span>{/if}</p>
      {#if previewAgent && focusMethod.agent}
        {@const goalName = graph.methodGoals?.[focusMethod.name ?? ''] ?? focusMethod.goal ?? ''}
        <button type="button" class="small" disabled={subPlanBusy} onclick={() => runSubPreview(focusMethod.agent ?? '', goalName)}>
          {subPlanBusy ? 'Planning…' : `Preview this agent's plan (${ag?.planner ?? 'goap'})`}
        </button>
        {#if subPlan?.for === focusMethod.agent}
          <p class="hint">
            {#if subPlan.issue}{subPlan.issue}
            {:else if subPlan.preview?.reached}✓ the goal already holds in this world
            {:else if subPlan.preview?.actions?.length}plan ({subPlan.preview.actions.length} step(s), cost {subPlan.preview.cost}): {subPlan.preview.actions
                .map((a) => a.name)
                .join(' → ')}
            {:else if subPlan.preview?.awaiting?.length}waiting on: {subPlan.preview.awaiting.join(', ')}
            {:else}no plan reaches the goal from this world{/if}
          </p>
        {/if}
      {/if}
      {#if ag?.actions?.length}
        <table class="acts">
          <thead><tr><th>Action</th><th>Kind</th><th>Needs</th><th>Makes</th></tr></thead>
          <tbody>
            {#each ag.actions as a (a.name)}
              <tr title={a.description}><td class="mono">{a.name}</td><td>{a.kind}</td><td class="mono">{conds(a.pre).join(' ')}</td><td class="mono">{conds(a.effects).join(' ')}</td></tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {:else}
      <p class="hint">Select a step to see what it needs, what it makes true, its roles and its guidance; select a method (under a step naming a capability) to focus on it. Double-click a nested process to open it.</p>
      {#if graph.description}<p>{graph.description}</p>{/if}
    {/if}
  </aside>
</div>

<style>
  .pg {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 320px;
    gap: 8px;
    height: 560px;
  }
  @media (max-width: 900px) {
    .pg {
      grid-template-columns: 1fr;
      height: auto;
    }
    .canvas {
      height: 460px;
    }
  }
  .canvas {
    position: relative;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    overflow: hidden;
    background: var(--surface);
    cursor: grab;
    touch-action: none;
  }
  .left {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
  }
  .canvas {
    flex: 1;
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    font-size: 0.8em;
    margin-bottom: 4px;
  }
  .legend i {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: 2px;
    margin-right: 4px;
    vertical-align: middle;
  }
  svg {
    color: var(--muted);
  }
  .edge {
    fill: none;
    stroke: var(--muted);
    stroke-width: 1.3;
    opacity: 0.55;
  }
  .edge.hot {
    stroke: var(--accent);
    color: var(--accent);
    opacity: 1;
    stroke-width: 2;
  }
  .node {
    cursor: pointer;
  }
  .node rect:first-of-type {
    fill: var(--bg);
    stroke-width: 1.5;
  }
  .node.sel rect:first-of-type {
    stroke-width: 3;
  }
  .node.dim {
    opacity: 0.35;
  }
  .name {
    font-weight: 600;
    font-size: 13px;
    fill: var(--text);
  }
  .sub {
    font-size: 11px;
    fill: var(--muted);
    font-family: var(--mono);
  }
  .node.planned rect:first-of-type {
    stroke-width: 3;
    stroke-dasharray: none;
  }
  .plan-badge {
    fill: var(--accent);
  }
  .plan-badge-text {
    font-size: 10px;
    font-weight: 700;
    fill: var(--bg);
  }
  .connector {
    stroke: var(--muted);
    stroke-width: 1.3;
    opacity: 0.6;
    fill: none;
  }
  .method-box rect {
    fill: var(--accent-soft);
    stroke: var(--accent);
    stroke-width: 1.2;
  }
  .method-box.sel rect {
    fill: var(--accent);
  }
  .method-box text {
    font-size: 10.5px;
    fill: var(--text);
  }
  .edge.planned {
    stroke: var(--accent);
    opacity: 1;
    stroke-width: 2.4;
  }
  .conditions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    font-size: 0.8em;
    margin-bottom: 4px;
    max-height: 72px;
    overflow: auto;
  }
  .cond {
    border: 1px dashed var(--border);
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text);
    font-size: 0.85em;
    padding: 1px 6px;
    cursor: pointer;
  }
  .cond.t {
    border-color: var(--accent);
    border-style: solid;
    background: var(--accent-soft);
  }
  .cond.f {
    border-color: #e45756;
    border-style: solid;
    background: #e4575622;
  }
  .plan-banner {
    font-size: 0.85em;
    margin-bottom: 4px;
    min-height: 1.2em;
  }
  .plan-banner .ok {
    color: #54a24b;
  }
  .focus {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 8px 10px;
    overflow: auto;
    background: var(--bg);
  }
  .focus h4 {
    margin: 0 0 6px;
  }
  .k {
    color: var(--muted);
    font-size: 0.85em;
    margin-right: 4px;
  }
  .acts {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85em;
  }
  .acts th,
  .acts td {
    text-align: left;
    padding: 2px 4px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
</style>
