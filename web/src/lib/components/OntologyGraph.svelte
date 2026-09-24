<script lang="ts">
  // Ontology graph of a domain: node types as boxes, link types as labelled
  // arrows (from → to), inheritance (extends) as dashed hollow-headed arrows
  // from the subtype to its parent. Pan, zoom and drag; click a type to
  // highlight its links, including the ones it inherits.
  import { untrack } from 'svelte';
  import { layoutGraph, type LEdge, type LNode } from '../graphLayout';

  interface TypeIn {
    name: string;
    extends?: string;
    description?: string;
    properties?: string;
  }
  interface LinkIn {
    name: string;
    from: string;
    to: string;
  }

  let {
    nodeTypes,
    linkTypes,
    onopen,
  }: {
    nodeTypes: TypeIn[];
    linkTypes: LinkIn[];
    /** double-click on a type or a link: index in the source list */
    onopen?: (kind: 'node' | 'link', index: number) => void;
  } = $props();

  interface GNode extends LNode {
    description: string;
    props: string[];
    parent: string;
    index: number;
  }

  let nodes = $state<GNode[]>([]);
  let sel = $state('');
  let hidden = $state<string[]>([]);
  let view = $state({ k: 1, x: 0, y: 0 });
  let autoFit = true;
  let W = $state(0);
  let H = $state(0);
  let svg = $state<SVGSVGElement>();

  const H_NODE = 34;
  const textW = (s: string, px: number) => s.length * px;

  // --- model -------------------------------------------------------------------------

  const signature = $derived(
    JSON.stringify([nodeTypes.map((n) => [n.name.trim(), (n.extends ?? '').trim()]), linkTypes.map((l) => [l.name, l.from, l.to])]),
  );

  function build() {
    const seen = new Set<string>();
    const list: GNode[] = [];
    nodeTypes.forEach((t, index) => {
      const id = t.name.trim();
      if (!id || seen.has(id)) return;
      seen.add(id);
      list.push({
        id,
        index,
        w: Math.max(84, textW(id, 7.2) + 24),
        h: H_NODE,
        x: 0,
        y: 0,
        description: t.description ?? '',
        props: (t.properties ?? '').split(',').map((p) => p.trim()).filter(Boolean),
        parent: (t.extends ?? '').trim(),
      });
    });
    const edges: LEdge[] = [];
    for (const n of list) if (n.parent && seen.has(n.parent)) edges.push({ a: n.id, b: n.parent, kind: 'extends' });
    for (const l of linkTypes) if (seen.has(l.from) && seen.has(l.to)) edges.push({ a: l.from, b: l.to, kind: 'link' });
    layoutGraph(list, edges);
    nodes = list;
    if (sel && !seen.has(sel)) sel = '';
    autoFit = true;
    fit();
  }

  $effect(() => {
    void signature;
    untrack(build);
  });

  const nodeMap = $derived(new Map(nodes.map((n) => [n.id, n])));

  /** Ancestors of a type, nearest first (cycle-safe). */
  function ancestors(id: string): string[] {
    const out: string[] = [];
    let cur = nodeMap.get(id)?.parent ?? '';
    while (cur && nodeMap.has(cur) && !out.includes(cur) && cur !== id) {
      out.push(cur);
      cur = nodeMap.get(cur)?.parent ?? '';
    }
    return out;
  }

  // --- colors ------------------------------------------------------------------------

  const HUES = [212, 28, 145, 340, 265, 175, 50, 300, 100, 0];
  const linkNames = $derived([...new Set(linkTypes.map((l) => l.name).filter(Boolean))]);
  const colorOf = (name: string) => `hsl(${HUES[Math.max(0, linkNames.indexOf(name)) % HUES.length]} 62% 56%)`;

  // --- geometry ----------------------------------------------------------------------

  type P = { x: number; y: number };

  function border(n: GNode, to: P): P {
    const dx = to.x - n.x;
    const dy = to.y - n.y;
    const t = 1 / Math.max(Math.abs(dx) / (n.w / 2 + 2), Math.abs(dy) / (n.h / 2 + 2), 0.0001);
    return { x: n.x + dx * t, y: n.y + dy * t };
  }

  function arrow(tip: P, from: P, len: number, half: number): { points: string; base: P } {
    const dx = tip.x - from.x;
    const dy = tip.y - from.y;
    const d = Math.max(Math.hypot(dx, dy), 0.0001);
    const ux = dx / d;
    const uy = dy / d;
    const base = { x: tip.x - ux * len, y: tip.y - uy * len };
    const l = { x: base.x - uy * half, y: base.y + ux * half };
    const r = { x: base.x + uy * half, y: base.y - ux * half };
    return { points: `${tip.x},${tip.y} ${l.x},${l.y} ${r.x},${r.y}`, base };
  }

  interface GEdge {
    key: string;
    kind: 'link' | 'extends';
    name: string;
    from: string;
    to: string;
    index: number;
    path: string;
    head: string;
    lx: number;
    ly: number;
    color: string;
  }

  const edges = $derived.by<GEdge[]>(() => {
    const out: GEdge[] = [];
    for (const n of nodes) {
      const p = nodeMap.get(n.parent);
      if (!p || p === n) continue;
      const s = border(n, p);
      const e = border(p, n);
      const a = arrow(e, s, 15, 7);
      out.push({ key: `x:${n.id}`, kind: 'extends', name: 'extends', from: n.id, to: p.id, index: n.index, path: `M${s.x},${s.y} L${a.base.x},${a.base.y}`, head: a.points, lx: 0, ly: 0, color: 'var(--muted)' });
    }
    // parallel links between two types are fanned out; self links are loops
    const groups = new Map<string, number[]>();
    const usable: { l: LinkIn; i: number }[] = [];
    linkTypes.forEach((l, i) => {
      if (!nodeMap.has(l.from) || !nodeMap.has(l.to)) return;
      usable.push({ l, i });
      const k = l.from < l.to ? `${l.from}\u0000${l.to}` : `${l.to}\u0000${l.from}`;
      groups.set(k, [...(groups.get(k) ?? []), i]);
    });
    for (const { l, i } of usable) {
      const a = nodeMap.get(l.from)!;
      const b = nodeMap.get(l.to)!;
      const k = l.from < l.to ? `${l.from}\u0000${l.to}` : `${l.to}\u0000${l.from}`;
      const grp = groups.get(k)!;
      const slot = grp.indexOf(i);
      const color = colorOf(l.name);
      if (a === b) {
        const top = a.y - a.h / 2;
        const rise = 46 + slot * 34;
        const sx = a.x + a.w * 0.12;
        const ex = a.x - a.w * 0.12;
        const tip = { x: ex, y: top - 1 };
        const ar = arrow(tip, { x: ex - 6, y: top - 22 }, 10, 4.5);
        out.push({ key: `l:${i}`, kind: 'link', name: l.name, from: l.from, to: l.to, index: i, path: `M${sx},${top} C${sx + 34},${top - rise} ${ex - 34},${top - rise} ${ar.base.x},${ar.base.y}`, head: ar.points, lx: a.x, ly: top - rise * 0.75 - 4, color });
        continue;
      }
      // normal of the canonical direction, so opposite links fan out on opposite sides
      const [c1, c2] = l.from < l.to ? [a, b] : [b, a];
      const dx = c2.x - c1.x;
      const dy = c2.y - c1.y;
      const d = Math.max(Math.hypot(dx, dy), 0.0001);
      const off = (slot - (grp.length - 1) / 2) * 46;
      const c = { x: (a.x + b.x) / 2 - (dy / d) * off, y: (a.y + b.y) / 2 + (dx / d) * off };
      const s = border(a, c);
      const e = border(b, c);
      const ar = arrow(e, c, 10, 4.5);
      out.push({ key: `l:${i}`, kind: 'link', name: l.name, from: l.from, to: l.to, index: i, path: `M${s.x},${s.y} Q${c.x},${c.y} ${ar.base.x},${ar.base.y}`, head: ar.points, lx: 0.25 * s.x + 0.5 * c.x + 0.25 * e.x, ly: 0.25 * s.y + 0.5 * c.y + 0.25 * e.y, color });
    }
    return out;
  });

  // --- selection ---------------------------------------------------------------------

  const chain = $derived(sel ? [sel, ...ancestors(sel)] : []);

  function edgeState(e: GEdge): 'on' | 'inherited' | 'dim' | 'none' {
    if (!sel) return 'none';
    if (e.kind === 'extends') return chain.includes(e.from) || e.to === sel ? 'on' : 'dim';
    if (e.from === sel || e.to === sel) return 'on';
    if (chain.includes(e.from) || chain.includes(e.to)) return 'inherited';
    return 'dim';
  }

  const touched = $derived.by(() => {
    const s = new Set<string>();
    if (!sel) return s;
    for (const id of chain) s.add(id);
    for (const e of edges) if (edgeState(e) !== 'dim' && !isHidden(e)) (s.add(e.from), s.add(e.to));
    return s;
  });

  const isHidden = (e: GEdge) => e.kind === 'link' && hidden.includes(e.name);

  const subtypes = $derived(sel ? nodes.filter((n) => n.parent === sel) : []);
  const selNode = $derived(sel ? nodeMap.get(sel) : undefined);
  // the details panel goes on the side opposite to the selected type
  const leftSide = $derived(!!selNode && view.x + selNode.x * view.k > W / 2);

  const inheritedProps = $derived(
    sel
      ? ancestors(sel).flatMap((a) => (nodeMap.get(a)?.props ?? []).map((p) => ({ p, from: a })))
      : [],
  );
  const linksOf = $derived.by(() => {
    if (!sel) return { out: [], inn: [], inh: [] as { e: GEdge; via: string }[] };
    const links = edges.filter((e) => e.kind === 'link');
    return {
      out: links.filter((e) => e.from === sel),
      inn: links.filter((e) => e.to === sel),
      inh: links.filter((e) => e.from !== sel && e.to !== sel).flatMap((e) => {
        const via = chain.find((c) => c !== sel && (c === e.from || c === e.to));
        return via ? [{ e, via }] : [];
      }),
    };
  });

  function toggleHidden(name: string) {
    hidden = hidden.includes(name) ? hidden.filter((h) => h !== name) : [...hidden, name];
  }

  // --- view --------------------------------------------------------------------------

  function fit() {
    if (!W || !H || !nodes.length) return;
    let x0 = Infinity;
    let y0 = Infinity;
    let x1 = -Infinity;
    let y1 = -Infinity;
    for (const n of nodes) {
      x0 = Math.min(x0, n.x - n.w / 2);
      x1 = Math.max(x1, n.x + n.w / 2);
      y0 = Math.min(y0, n.y - n.h / 2 - 70);
      y1 = Math.max(y1, n.y + n.h / 2);
    }
    const pad = 36;
    const k = Math.min((W - 2 * pad) / (x1 - x0), (H - 2 * pad) / (y1 - y0), 1.3);
    view = { k, x: W / 2 - ((x0 + x1) / 2) * k, y: H / 2 - ((y0 + y1) / 2) * k };
  }

  $effect(() => {
    void W;
    void H;
    if (autoFit) untrack(fit);
  });

  function relayout() {
    build();
  }

  function zoomAt(factor: number, cx = W / 2, cy = H / 2) {
    autoFit = false;
    const k = Math.min(3, Math.max(0.15, view.k * factor));
    const f = k / view.k;
    view = { k, x: cx - (cx - view.x) * f, y: cy - (cy - view.y) * f };
  }

  function wheel(node: SVGSVGElement) {
    const h = (e: WheelEvent) => {
      e.preventDefault();
      const r = node.getBoundingClientRect();
      zoomAt(Math.exp(-e.deltaY * 0.0015), e.clientX - r.left, e.clientY - r.top);
    };
    node.addEventListener('wheel', h, { passive: false });
    return { destroy: () => node.removeEventListener('wheel', h) };
  }

  // --- pointer -----------------------------------------------------------------------

  let drag: { id: string; sx: number; sy: number; ox: number; oy: number; moved: boolean } | undefined;
  let pan: { sx: number; sy: number; vx: number; vy: number; moved: boolean } | undefined;

  function nodeDown(e: PointerEvent, n: GNode) {
    e.stopPropagation();
    svg?.setPointerCapture(e.pointerId);
    drag = { id: n.id, sx: e.clientX, sy: e.clientY, ox: n.x, oy: n.y, moved: false };
  }

  function bgDown(e: PointerEvent) {
    svg?.setPointerCapture(e.pointerId);
    pan = { sx: e.clientX, sy: e.clientY, vx: view.x, vy: view.y, moved: false };
  }

  function move(e: PointerEvent) {
    if (drag) {
      const dx = e.clientX - drag.sx;
      const dy = e.clientY - drag.sy;
      if (!drag.moved && Math.hypot(dx, dy) < 4) return;
      drag.moved = true;
      const n = nodeMap.get(drag.id);
      if (n) {
        n.x = drag.ox + dx / view.k;
        n.y = drag.oy + dy / view.k;
      }
    } else if (pan) {
      const dx = e.clientX - pan.sx;
      const dy = e.clientY - pan.sy;
      if (!pan.moved && Math.hypot(dx, dy) < 4) return;
      pan.moved = true;
      autoFit = false;
      view = { ...view, x: pan.vx + dx, y: pan.vy + dy };
    }
  }

  function up() {
    if (drag && !drag.moved) sel = sel === drag.id ? '' : drag.id;
    else if (pan && !pan.moved) sel = '';
    drag = pan = undefined;
  }

  function key(e: KeyboardEvent, n: GNode) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      sel = sel === n.id ? '' : n.id;
    }
  }

  const stateClass = (e: GEdge) => `${e.kind} ${edgeState(e)}`;
  const labelW = (s: string) => textW(s, 6.4) + 12;
</script>

<div class="wrap">
  <div class="canvas" bind:clientWidth={W} bind:clientHeight={H}>
    {#if nodes.length === 0}
      <p class="empty">No node types to draw.</p>
    {:else}
      <svg
        bind:this={svg}
        role="application"
        aria-label="Ontology graph"
        onpointerdown={bgDown}
        onpointermove={move}
        onpointerup={up}
        onpointercancel={up}
        use:wheel
      >
        <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
          {#each edges as e (e.key)}
            {#if !isHidden(e)}
              <g class="edge {stateClass(e)}" style={`--c:${e.color}`}>
                <path class="hit" d={e.path} />
                <path class="line" d={e.path} />
                <polygon class="head" points={e.head} />
                <title>{e.kind === 'extends' ? `${e.from} extends ${e.to}` : `${e.from} —${e.name}→ ${e.to}`}</title>
              </g>
            {/if}
          {/each}

          {#each nodes as n (n.id)}
            <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
            <g
              class="node"
              class:sel={sel === n.id}
              class:dim={sel && !touched.has(n.id)}
              class:root={!n.parent}
              transform={`translate(${n.x} ${n.y})`}
              role="button"
              tabindex="0"
              aria-label={`Type ${n.id}`}
              aria-pressed={sel === n.id}
              onpointerdown={(e) => nodeDown(e, n)}
              onkeydown={(e) => key(e, n)}
              ondblclick={() => onopen?.('node', n.index)}
            >
              <rect x={-n.w / 2} y={-n.h / 2} width={n.w} height={n.h} rx="7" />
              <text y="4.5" text-anchor="middle">{n.id}</text>
              {#if n.description}<title>{n.description}</title>{/if}
            </g>
          {/each}

          {#each edges as e (e.key)}
            {#if e.kind === 'link' && !isHidden(e)}
              <g
                class="label {edgeState(e)}"
                style={`--c:${e.color}`}
                transform={`translate(${e.lx} ${e.ly})`}
                ondblclick={() => onopen?.('link', e.index)}
                role="presentation"
              >
                <rect x={-labelW(e.name) / 2} y="-9" width={labelW(e.name)} height="18" rx="9" />
                <text y="4" text-anchor="middle">{e.name}</text>
              </g>
            {/if}
          {/each}
        </g>
      </svg>

      <div class="tools" role="toolbar" aria-label="Graph view">
        <button type="button" class="small" onclick={() => zoomAt(1.25)} aria-label="Zoom in">+</button>
        <button type="button" class="small" onclick={() => zoomAt(0.8)} aria-label="Zoom out">−</button>
        <button
          type="button"
          class="small"
          onclick={() => {
            autoFit = true;
            fit();
          }}>Fit</button
        >
        <button type="button" class="small" title="Recompute the layout" onclick={relayout}>Re-layout</button>
      </div>
      <div class="key" aria-hidden="true">
        <svg width="46" height="12"><line x1="2" y1="6" x2="34" y2="6" stroke="var(--accent)" stroke-width="2" /><polygon points="44,6 35,2 35,10" fill="var(--accent)" /></svg>
        link (from → to)
        <svg width="46" height="12"><line x1="2" y1="6" x2="30" y2="6" stroke="var(--muted)" stroke-width="1.5" stroke-dasharray="5 4" /><polygon points="44,6 30,1 30,11" fill="var(--surface)" stroke="var(--muted)" /></svg>
        extends (subtype → parent)
      </div>
    {/if}
  </div>

  <aside class="side" class:overlay={!!selNode} class:left={leftSide} aria-label="Details">
    {#if selNode}
      <div class="row">
        <h4>{selNode.id}</h4>
        <span class="grow"></span>
        <button type="button" class="small ghost" onclick={() => (sel = '')} aria-label="Clear selection">×</button>
      </div>
      {#if selNode.description}<p class="desc">{selNode.description}</p>{/if}
      {#if chain.length > 1}
        <p class="hint">Extends: {chain.slice(1).join(' → ')}</p>
      {/if}
      {#if selNode.props.length || inheritedProps.length}
        <h5>Properties</h5>
        <p class="props">
          {#each selNode.props as p (p)}<code>{p}</code>{/each}
          {#each inheritedProps as ip (ip.from + ip.p)}<code class="inh" title={`inherited from ${ip.from}`}>{ip.p}</code>{/each}
        </p>
      {/if}
      {#if subtypes.length}
        <h5>Subtypes</h5>
        <p class="props">{#each subtypes as s (s.id)}<button type="button" class="link" onclick={() => (sel = s.id)}>{s.id}</button>{/each}</p>
      {/if}
      <h5>Links</h5>
      {#if !linksOf.out.length && !linksOf.inn.length && !linksOf.inh.length}
        <p class="hint">No link touches this type.</p>
      {/if}
      <ul class="links">
        {#each linksOf.out as e (e.key)}
          <li><span class="sw" style={`background:${e.color}`}></span><code>{e.name}</code> →
            <button type="button" class="link" onclick={() => (sel = e.to)}>{e.to}</button></li>
        {/each}
        {#each linksOf.inn as e (e.key)}
          <li><span class="sw" style={`background:${e.color}`}></span>
            <button type="button" class="link" onclick={() => (sel = e.from)}>{e.from}</button> → <code>{e.name}</code></li>
        {/each}
        {#each linksOf.inh as { e, via } (e.key)}
          <li class="inh"><span class="sw" style={`background:${e.color}`}></span>
            {e.from} → <code>{e.name}</code> → {e.to} <span class="hint">(via {via})</span></li>
        {/each}
      </ul>
    {:else}
      <h4>{nodes.length} node types · {linkTypes.length} link types</h4>
      <p class="hint">Click a type to highlight its links (inherited ones included). Drag to rearrange, scroll to zoom, double-click to edit.</p>
      <h5>Link types</h5>
      <ul class="links">
        {#each linkNames as name (name)}
          <li>
            <button type="button" class="chipbtn" class:off={hidden.includes(name)} aria-pressed={!hidden.includes(name)} onclick={() => toggleHidden(name)} title="Show / hide">
              <span class="sw" style={`background:${colorOf(name)}`}></span>{name}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </aside>
</div>

<style>
  .wrap {
    position: relative;
  }
  .canvas {
    position: relative;
    height: clamp(380px, 62vh, 720px);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm, 6px);
    background:
      radial-gradient(circle, var(--border) 1px, transparent 1px) 0 0 / 22px 22px,
      var(--surface);
    overflow: hidden;
  }
  .canvas > svg {
    width: 100%;
    height: 100%;
    display: block;
    touch-action: none;
    cursor: grab;
    color: var(--text);
    user-select: none;
  }
  .tools {
    position: absolute;
    top: 8px;
    left: 8px;
    display: flex;
    gap: 4px;
  }
  .key {
    position: absolute;
    left: 8px;
    bottom: 8px;
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.78rem;
    color: var(--muted);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 2px 8px;
    flex-wrap: wrap;
  }
  .node {
    cursor: pointer;
    outline: none;
  }
  .node rect {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1.5;
  }
  .node.root rect {
    stroke: var(--muted);
  }
  .node text {
    fill: currentColor;
    font-size: 12px;
    font-weight: 600;
    pointer-events: none;
  }
  .node:hover rect,
  .node:focus-visible rect {
    stroke: var(--accent);
  }
  .node.sel rect {
    stroke: var(--accent);
    stroke-width: 2.5;
    fill: var(--accent-soft, var(--hover));
  }
  .node.dim {
    opacity: 0.28;
  }
  .edge .hit {
    fill: none;
    stroke: transparent;
    stroke-width: 12;
  }
  .edge .line {
    fill: none;
    stroke: var(--c);
    stroke-width: 1.8;
  }
  .edge .head {
    fill: var(--c);
  }
  .edge.extends .line {
    stroke-dasharray: 6 4;
    stroke-width: 1.6;
  }
  .edge.extends .head {
    fill: var(--surface);
    stroke: var(--c);
    stroke-width: 1.5;
  }
  .edge.on .line {
    stroke-width: 3;
  }
  .edge.inherited .line {
    stroke-dasharray: 2 4;
    stroke-width: 2.4;
  }
  .edge.dim {
    opacity: 0.1;
  }
  .label rect {
    fill: var(--surface);
    stroke: var(--c);
    stroke-width: 1.2;
  }
  .label text {
    fill: currentColor;
    font-size: 11px;
    pointer-events: none;
  }
  .label {
    cursor: default;
  }
  .label.dim {
    opacity: 0.12;
  }
  .side {
    padding: 0.5rem 0.1rem;
  }
  .side.overlay {
    position: absolute;
    top: 8px;
    right: 8px;
    width: 260px;
    max-height: calc(clamp(380px, 62vh, 720px) - 16px);
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm, 6px);
    padding: 0.6rem 0.75rem;
    background: var(--surface);
    box-shadow: 0 6px 20px rgb(0 0 0 / 0.25);
  }
  .side.overlay.left {
    right: auto;
    left: 8px;
    top: 44px;
    max-height: calc(clamp(380px, 62vh, 720px) - 52px);
  }
  .side:not(.overlay) .links {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
  }
  .side h4 {
    margin: 0;
    font-size: 1rem;
  }
  .side h5 {
    margin: 0.8rem 0 0.25rem;
    font-size: 0.8rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .desc {
    margin: 0.3rem 0;
  }
  .props {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    margin: 0;
  }
  .props code.inh {
    opacity: 0.65;
    font-style: italic;
  }
  .links {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.3rem;
  }
  .links li.inh {
    opacity: 0.75;
    font-size: 0.92em;
  }
  .sw {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: 3px;
    margin-right: 0.35rem;
    vertical-align: -1px;
  }
  .link {
    border: none;
    background: none;
    padding: 0;
    min-height: 0;
    color: var(--accent);
    text-decoration: underline;
    font-weight: 500;
  }
  .chipbtn {
    display: inline-flex;
    align-items: center;
    border-radius: 999px;
    padding: 0 0.6rem;
    min-height: 0;
    font-family: var(--mono);
    font-size: 0.85rem;
    font-weight: 400;
  }
  .chipbtn.off {
    opacity: 0.45;
    text-decoration: line-through;
  }
</style>
