<script lang="ts">
  // Graph centered on a node (or, in link view, two nodes — a link's endpoints): parents left, children right by
  // bias, laid out by a live d3-force simulation rather than fixed rows. Click a node to recenter, double-click to
  // open it, drag to rearrange; click a link to select it (info only, never navigates). Wheel to zoom, drag the
  // background to pan.
  import { forceCollide, forceLink, forceManyBody, forceSimulation, forceX, forceY, type ForceLink, type Simulation } from 'd3-force';
  import { untrack } from 'svelte';
  import type { GraphIndex } from '../graphIndex';

  let {
    index,
    center,
    onrecenter,
    onopen,
    maxDepth = 2,
    suspect,
    secondaryCenter,
    onLinkSelect,
  }: {
    index: GraphIndex;
    center: string;
    onrecenter: (id: string) => void;
    onopen: (id: string) => void;
    /** 1: the index only holds the direct neighbours of the center (no level choice) */
    maxDepth?: 1 | 2;
    /** ids of the suspect links (drawn dashed) */
    suspect?: Set<string>;
    /** a link's other endpoint, in link view: drawn as a second centered node, its edge to `center` emphasized */
    secondaryCenter?: string;
    onLinkSelect?: (linkId: string) => void;
  } = $props();

  let depth = $state<1 | 2>(1);

  const W = 168;
  const H = 34;
  const GX = 90;
  const CAP = 14;

  interface Box {
    id: string;
    col: number;
    x: number;
    y: number;
    more?: number;
  }

  interface SimNode {
    id: string;
    col: number;
    x: number;
    y: number;
    vx?: number;
    vy?: number;
    fx?: number | null;
    fy?: number | null;
  }

  const cols = $derived.by(() => {
    const placed = new Map<string, number>([[center, 0]]);
    const byCol = new Map<number, string[]>([[0, [center]]]);
    const put = (id: string, col: number) => {
      if (placed.has(id)) return false;
      placed.set(id, col);
      byCol.set(col, [...(byCol.get(col) ?? []), id]);
      return true;
    };
    const expand = (from: number, dir: 'out' | 'in', sign: 1 | -1) => {
      for (const id of byCol.get(from) ?? []) {
        for (const l of index[dir].get(id) ?? []) put((dir === 'out' ? l.to?.id : l.from?.id) ?? '', from + sign);
      }
    };
    expand(0, 'in', -1);
    expand(0, 'out', 1);
    if (depth === 2 && maxDepth === 2) {
      expand(-1, 'in', -1);
      expand(1, 'out', 1);
    }
    return { placed, byCol };
  });

  /** which nodes to show, and which column each belongs in; x/y come from the live simulation below */
  const layout = $derived.by(() => {
    const raw: { id: string; col: number; more?: number }[] = [];
    for (const [col, ids] of cols.byCol) {
      const shown = ids.filter((id) => index.nodes.has(id)).sort((a, b) => (index.nodes.get(a)?.key ?? '').localeCompare(index.nodes.get(b)?.key ?? ''));
      const list = shown.slice(0, CAP);
      list.forEach((id) => raw.push({ id, col }));
      if (shown.length > CAP) raw.push({ id: `…${col}`, col, more: shown.length - CAP });
    }
    const idSet = new Set(raw.map((b) => b.id));
    const simLinks: { source: string; target: string }[] = [];
    const seenL = new Set<string>();
    for (const b of raw) {
      if (b.more) continue;
      for (const l of index.out.get(b.id) ?? []) {
        const tid = l.to?.id ?? '';
        if (!idSet.has(tid)) continue;
        const key = l.id ?? `${b.id}>${tid}`;
        if (seenL.has(key)) continue;
        seenL.add(key);
        simLinks.push({ source: b.id, target: tid });
      }
    }
    return { raw, simLinks };
  });

  /** live positions, mutated in place by the running simulation every tick (a $state array of plain objects: each
   * write to a node's x/y is tracked, so the SVG re-renders on its own, no manual tick counter needed) */
  let simNodes = $state<SimNode[]>([]);
  let sim: Simulation<SimNode, { source: string; target: string }> | undefined;
  let linkForce: ForceLink<SimNode, { source: string; target: string }> | undefined;
  let draggingId = $state<string | undefined>(undefined);

  $effect(() => {
    const { raw, simLinks } = layout;
    const centerId = center;
    const secondaryId = secondaryCenter;
    untrack(() => {
      const byId = new Map(simNodes.map((n) => [n.id, n]));
      const wanted = new Set(raw.map((b) => b.id));
      for (let i = simNodes.length - 1; i >= 0; i--) if (!wanted.has(simNodes[i].id)) simNodes.splice(i, 1);
      for (const b of raw) {
        const n = byId.get(b.id);
        if (n) n.col = b.col;
        else simNodes.push({ id: b.id, col: b.col, x: b.col * (W + GX) + (Math.random() - 0.5) * 10, y: (Math.random() - 0.5) * 40 });
      }
      for (const n of simNodes) {
        if (n.id === draggingId) continue;
        if (n.id === centerId) {
          n.fx = 0;
          n.fy = 0;
        } else if (n.id === secondaryId) {
          n.fx = n.col * (W + GX);
          n.fy = 0;
        } else {
          n.fx = null;
          n.fy = null;
        }
      }

      if (!sim) {
        linkForce = forceLink<SimNode, { source: string; target: string }>(simLinks).id((d) => d.id).distance(W + GX).strength(0.35);
        sim = forceSimulation(simNodes)
          .force('link', linkForce)
          .force('charge', forceManyBody().strength(-260))
          .force('collide', forceCollide(Math.max(W, H) / 1.6))
          .force('x', forceX<SimNode>((d) => d.col * (W + GX)).strength(0.22))
          .force('y', forceY(0).strength(0.06));
      } else {
        sim.nodes(simNodes);
        linkForce?.links(simLinks);
        sim.alpha(Math.max(sim.alpha(), 0.6)).restart();
      }
    });
  });

  $effect(() => () => sim?.stop());

  const boxes = $derived<Box[]>(layout.raw.map((b) => {
    const n = simNodes.find((s) => s.id === b.id);
    return { id: b.id, col: b.col, x: n?.x ?? b.col * (W + GX), y: n?.y ?? 0, more: b.more };
  }));

  const boxOf = $derived(new Map(boxes.map((b) => [b.id, b])));

  function toSvgPoint(svgEl: SVGSVGElement, clientX: number, clientY: number) {
    const pt = svgEl.createSVGPoint();
    pt.x = clientX;
    pt.y = clientY;
    const ctm = svgEl.getScreenCTM();
    if (!ctm) return { x: 0, y: 0 };
    const p = pt.matrixTransform(ctm.inverse());
    return { x: p.x, y: p.y };
  }

  let suppressClick = false;

  function dragNode(e: PointerEvent, id: string) {
    const svgEl = (e.currentTarget as SVGGraphicsElement).ownerSVGElement;
    if (!svgEl || id === center || id === secondaryCenter) return;
    e.stopPropagation();
    draggingId = id;
    const n = simNodes.find((s) => s.id === id);
    if (!n) return;
    const startX = e.clientX;
    const startY = e.clientY;
    sim?.alphaTarget(0.3).restart();
    const move = (ev: PointerEvent) => {
      if (Math.hypot(ev.clientX - startX, ev.clientY - startY) > 4) suppressClick = true;
      const p = toSvgPoint(svgEl, ev.clientX, ev.clientY);
      n.fx = p.x;
      n.fy = p.y;
      n.x = p.x;
      n.y = p.y;
    };
    const up = () => {
      sim?.alphaTarget(0);
      draggingId = undefined;
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  const HUES = [212, 28, 145, 340, 265, 175, 50, 300, 100, 0];
  const linkTypes = $derived([...new Set(index.list.length ? [...index.out.values()].flat().map((l) => l.type ?? '') : [])].sort());
  const colorOf = (t: string | undefined) => `hsl(${HUES[Math.max(0, linkTypes.indexOf(t ?? '')) % HUES.length]} 60% 55%)`;

  interface Edge {
    key: string;
    linkId?: string;
    d: string;
    head: string;
    lx: number;
    ly: number;
    type: string;
    color: string;
    touchesCenter: boolean;
    touchesBoth: boolean;
    suspect: boolean;
  }

  const edges = $derived.by<Edge[]>(() => {
    const out: Edge[] = [];
    const seen = new Set<string>();
    for (const b of boxes) {
      for (const l of index.out.get(b.id) ?? []) {
        const t = boxOf.get(l.to?.id ?? '');
        if (!t || b.more) continue;
        const key = l.id ?? `${b.id}>${t.id}:${l.type}`;
        if (seen.has(key)) continue;
        seen.add(key);
        const fwd = t.x > b.x;
        const same = t.x === b.x;
        const x1 = same ? b.x : fwd ? b.x + W / 2 : b.x - W / 2;
        const x2 = same ? t.x : fwd ? t.x - W / 2 : t.x + W / 2;
        const y1 = b.y + (same ? (t.y > b.y ? H / 2 : -H / 2) : 0);
        const y2 = t.y + (same ? (t.y > b.y ? -H / 2 : H / 2) : 0);
        const cx = same ? x1 + (W / 2 + 26) : (x1 + x2) / 2;
        const cy = (y1 + y2) / 2;
        const dx = x2 - cx;
        const dy = y2 - cy;
        const len = Math.max(Math.hypot(dx, dy), 1);
        const ux = dx / len;
        const uy = dy / len;
        const bx = x2 - ux * 9;
        const by = y2 - uy * 9;
        const ends = new Set([b.id, t.id]);
        out.push({
          key,
          linkId: l.id,
          d: `M${x1},${y1} Q${cx},${cy} ${bx},${by}`,
          head: `${x2},${y2} ${bx - uy * 4},${by + ux * 4} ${bx + uy * 4},${by - ux * 4}`,
          lx: 0.25 * x1 + 0.5 * cx + 0.25 * x2,
          ly: 0.25 * y1 + 0.5 * cy + 0.25 * y2,
          type: l.type ?? '',
          color: colorOf(l.type),
          touchesCenter: ends.has(center) || (!!secondaryCenter && ends.has(secondaryCenter)),
          touchesBoth: !!secondaryCenter && ends.has(center) && ends.has(secondaryCenter),
          suspect: !!suspect?.has(l.id ?? ''),
        });
      }
    }
    return out;
  });

  const autoView = $derived.by(() => {
    if (!boxes.length) return { x: 0, y: 0, w: 400, h: 120 };
    const x0 = Math.min(...boxes.map((b) => b.x)) - W / 2 - 24;
    const x1 = Math.max(...boxes.map((b) => b.x)) + W / 2 + 24;
    const y0 = Math.min(...boxes.map((b) => b.y)) - H / 2 - 24;
    const y1 = Math.max(...boxes.map((b) => b.y)) + H / 2 + 24;
    return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
  });

  /** the user's own pan/zoom, kept until recentering (a structural change) replaces the picture under it */
  let viewOverride = $state<{ x: number; y: number; w: number; h: number } | null>(null);
  const view = $derived(viewOverride ?? autoView);

  $effect(() => {
    void center;
    void secondaryCenter;
    viewOverride = null;
  });

  let svgRef: SVGSVGElement | undefined;

  function onWheel(e: WheelEvent) {
    if (!svgRef) return;
    e.preventDefault();
    const rect = svgRef.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    const v = view;
    const fracX = (e.clientX - rect.left) / rect.width;
    const fracY = (e.clientY - rect.top) / rect.height;
    const factor = Math.exp(-e.deltaY * 0.0015);
    const newW = Math.min(Math.max(v.w / factor, 200), 6000);
    const newH = Math.min(Math.max(v.h / factor, 100), 4000);
    const cx = v.x + fracX * v.w;
    const cy = v.y + fracY * v.h;
    viewOverride = { x: cx - fracX * newW, y: cy - fracY * newH, w: newW, h: newH };
  }

  function panBackground(e: PointerEvent) {
    if (e.button !== 0 || !svgRef) return;
    const startX = e.clientX;
    const startY = e.clientY;
    const startView = view;
    const rect = svgRef.getBoundingClientRect();
    const move = (ev: PointerEvent) => {
      const dx = ((ev.clientX - startX) / rect.width) * startView.w;
      const dy = ((ev.clientY - startY) / rect.height) * startView.h;
      viewOverride = { x: startView.x - dx, y: startView.y - dy, w: startView.w, h: startView.h };
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  const trunc = (s: string, n = 22) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

  function selectLink(e: Edge) {
    if (e.linkId) onLinkSelect?.(e.linkId);
  }
</script>

<div class="graph">
  <div class="tools" role="group" aria-label="Graph depth">
    {#if maxDepth === 2}
      <span class="hint">Levels</span>
      <button type="button" class="small" class:primary={depth === 1} onclick={() => (depth = 1)}>1</button>
      <button type="button" class="small" class:primary={depth === 2} onclick={() => (depth = 2)}>2</button>
    {/if}
    <span class="hint legend">parents ← node → children</span>
    {#if viewOverride}<button type="button" class="small" onclick={() => (viewOverride = null)}>Fit</button>{/if}
  </div>
  <div class="scroll">
    <svg
      bind:this={svgRef}
      viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
      style={`width:${Math.max(view.w, 320)}px;height:${Math.max(view.h, 120)}px`}
      role="img"
      aria-label="Graph of the node and its neighbours"
      onwheel={onWheel}
      onpointerdown={panBackground}
    >
      <rect x={view.x} y={view.y} width={view.w} height={view.h} class="backdrop" />
      {#each edges as e (e.key)}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <g
          class="edge"
          class:dim={!e.touchesCenter}
          class:suspect={e.suspect}
          class:selected={e.touchesBoth}
          style={`--c:${e.color}`}
          role={onLinkSelect ? 'button' : undefined}
          tabindex={onLinkSelect ? 0 : undefined}
          onclick={() => selectLink(e)}
          onkeydown={(ev) => (ev.key === 'Enter' ? selectLink(e) : undefined)}
        >
          <path d={e.d} />
          <polygon points={e.head} />
          <g transform={`translate(${e.lx} ${e.ly})`}>
            <rect x={-(e.type.length * 3.2 + 6)} y="-8" width={e.type.length * 6.4 + 12} height="16" rx="8" />
            <text y="3.5" text-anchor="middle">{e.type}</text>
          </g>
          {#if onLinkSelect}<title>{e.type} — click for its properties.</title>{/if}
        </g>
      {/each}
      {#each boxes as b (b.id)}
        {#if b.more}
          <g transform={`translate(${b.x} ${b.y})`} class="more"><text text-anchor="middle" y="4">+{b.more} more</text></g>
        {:else}
          {@const n = index.nodes.get(b.id)}
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <g
            class="node"
            class:center={b.id === center || b.id === secondaryCenter}
            class:dragging={draggingId === b.id}
            transform={`translate(${b.x} ${b.y})`}
            role="button"
            tabindex="0"
            aria-label={`${n?.key} (${n?.type})`}
            onclick={() => {
              if (suppressClick) {
                suppressClick = false;
                return;
              }
              onrecenter(b.id);
            }}
            ondblclick={() => onopen(b.id)}
            onkeydown={(e) => (e.key === 'Enter' ? onrecenter(b.id) : undefined)}
            onpointerdown={(e) => dragNode(e, b.id)}
          >
            <rect x={-W / 2} y={-H / 2} width={W} height={H} rx="7" />
            <text y="-2" text-anchor="middle" class="k">{trunc(n?.key ?? '')}</text>
            <text y="11" text-anchor="middle" class="t">{trunc(`${n?.type ?? ''}${n?.state ? ` · ${n.state}` : ''}`, 26)}</text>
            <title>{n?.key} — {n?.type}{n?.state ? ` (${n.state})` : ''}. Click to recenter, double-click to open.</title>
          </g>
        {/if}
      {/each}
    </svg>
  </div>
  {#if boxes.length === 1}<p class="hint">This node has no link yet.</p>{/if}
</div>

<style>
  .tools {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin-bottom: 0.4rem;
  }
  .legend {
    margin-left: 0.8rem;
  }
  .scroll {
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    max-height: 60vh;
  }
  svg {
    display: block;
    color: var(--text);
    touch-action: none;
  }
  .backdrop {
    fill: transparent;
    cursor: grab;
  }
  .node {
    cursor: pointer;
    outline: none;
    touch-action: none;
  }
  .node.dragging {
    cursor: grabbing;
  }
  .node rect {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1.5;
  }
  .node:hover rect,
  .node:focus-visible rect {
    stroke: var(--accent);
  }
  .node.center rect {
    stroke: var(--accent);
    stroke-width: 2.5;
    fill: var(--hover);
  }
  .node .k {
    fill: currentColor;
    font-size: 12px;
    font-weight: 600;
    pointer-events: none;
  }
  .node .t {
    fill: var(--muted);
    font-size: 10px;
    pointer-events: none;
  }
  .edge {
    cursor: pointer;
    outline: none;
  }
  .edge path {
    fill: none;
    stroke: var(--c);
    stroke-width: 1.8;
  }
  .edge polygon {
    fill: var(--c);
  }
  .edge rect {
    fill: var(--surface);
    stroke: var(--c);
  }
  .edge text {
    fill: currentColor;
    font-size: 10px;
  }
  .edge:hover path,
  .edge:focus-visible path {
    stroke-width: 3;
  }
  .edge.selected path {
    stroke-width: 3.5;
  }
  .edge.selected rect {
    stroke-width: 2;
  }
  .edge.suspect path {
    stroke: var(--warn);
    stroke-dasharray: 5 3;
  }
  .edge.suspect polygon {
    fill: var(--warn);
  }
  .edge.dim {
    opacity: 0.45;
  }
  .more text {
    fill: var(--muted);
    font-size: 11px;
  }
</style>
