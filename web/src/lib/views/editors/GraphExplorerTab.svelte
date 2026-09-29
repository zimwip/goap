<script lang="ts">
  // Graph explorer (ADR 0036 §4): the whole graph as it is — the head of main of every namespace (organisation,
  // platform, methodology, and the domains' data), with the links between them — laid out by a force simulation
  // (d3-force). Filter by namespace or text, start from the organisation (units, users, and what they own), select a
  // node to see its neighbourhood, focus on it, or open it in its editor.
  import { untrack } from 'svelte';
  import { forceSimulation, forceLink, forceManyBody, forceCenter, forceCollide, forceX, forceY, type SimulationNodeDatum, type SimulationLinkDatum } from 'd3-force';
  import type { Tab } from '../../shell/types';
  import { graph as graphApi, errorMessage, type GraphNode, type Link } from '../../api';
  import { openNode } from '../../nodeEditors';

  let { tab }: { tab: Tab } = $props();
  void untrack(() => tab);

  interface N extends SimulationNodeDatum {
    id: string;
    node: GraphNode;
    label: string;
    ns: string;
    degree: number;
  }
  interface L extends SimulationLinkDatum<N> {
    id: string;
    type: string;
    source: N | string;
    target: N | string;
  }

  let all = $state<{ nodes: GraphNode[]; links: Link[] }>({ nodes: [], links: [] });
  let namespaces = $state<string[]>([]);
  let shown = $state<Record<string, boolean>>({});
  let loading = $state(true);
  let error = $state('');
  let filter = $state('');
  let focusOn = $state<{ id: string; depth: number } | null>(null);
  let preset = $state<'all' | 'organisation' | 'ownership'>('all');
  const MAX = 2500;

  async function load() {
    loading = true;
    error = '';
    try {
      const ns = (await graphApi.listNamespaces()).namespaces ?? [];
      const heads = await Promise.all(ns.map((n) => graphApi.getBranch(n, 'main').catch(() => ({ head: undefined }))));
      const graphs = await Promise.all(heads.map((h) => (h.head?.id ? graphApi.getBaselineGraph(h.head.id) : Promise.resolve({ nodes: [], links: [] }))));
      const nodes = new Map<string, GraphNode>();
      const links: Link[] = [];
      for (const g of graphs) {
        for (const n of g.nodes ?? []) if (n.id && !n.deleted && n.props?.removed !== true) nodes.set(n.id, n);
        links.push(...(g.links ?? []));
      }
      all = { nodes: [...nodes.values()], links };
      namespaces = [...new Set(all.nodes.map((n) => n.namespace ?? ''))].sort();
      shown = Object.fromEntries(namespaces.map((n) => [n, n !== 'methodology']));
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    void load();
  });

  function applyPreset(p: typeof preset) {
    preset = p;
    focusOn = null;
    if (p === 'all') shown = Object.fromEntries(namespaces.map((n) => [n, n !== 'methodology']));
    else shown = Object.fromEntries(namespaces.map((n) => [n, n === 'organisation' || (p === 'ownership' && n !== 'methodology' && n !== 'platform')]));
  }

  const labelOf = (n: GraphNode) => {
    const p = (n.props ?? {}) as Record<string, unknown>;
    return String(p.name ?? p.title ?? p.displayName ?? n.key ?? n.id ?? '');
  };

  // the nodes and links to draw, laid out
  const view = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    // the organisation presets show its structure: units, users, adapters (not the access rules)
    let nodes = all.nodes.filter((n) => shown[n.namespace ?? ''] && (preset === 'all' || n.type !== 'organisation@Policy'));
    const ids = new Set(nodes.map((n) => n.id ?? ''));
    let links = all.links.filter((l) => ids.has(l.from?.id ?? '') && ids.has(l.to?.id ?? '') && l.from?.id !== l.to?.id);
    if (preset === 'ownership') {
      // the units and users, and the nodes linked to them (owner, member_of)
      const org = new Set(nodes.filter((n) => n.namespace === 'organisation').map((n) => n.id ?? ''));
      const keep = new Set(org);
      for (const l of links) {
        if (org.has(l.to?.id ?? '')) keep.add(l.from?.id ?? '');
        if (org.has(l.from?.id ?? '')) keep.add(l.to?.id ?? '');
      }
      nodes = nodes.filter((n) => keep.has(n.id ?? ''));
    }
    if (focusOn) {
      const around = new Set([focusOn.id]);
      for (let d = 0; d < focusOn.depth; d++) {
        for (const l of links) {
          if (around.has(l.from?.id ?? '')) around.add(l.to?.id ?? '');
          if (around.has(l.to?.id ?? '')) around.add(l.from?.id ?? '');
        }
      }
      nodes = nodes.filter((n) => around.has(n.id ?? ''));
    }
    if (q) {
      const hit = new Set(nodes.filter((n) => `${n.key} ${labelOf(n)} ${n.type}`.toLowerCase().includes(q)).map((n) => n.id ?? ''));
      // the hits and their neighbours
      for (const l of links) {
        if (hit.has(l.from?.id ?? '')) hit.add(l.to?.id ?? '');
        if (hit.has(l.to?.id ?? '')) hit.add(l.from?.id ?? '');
      }
      nodes = nodes.filter((n) => hit.has(n.id ?? ''));
    }
    const truncated = nodes.length > MAX;
    nodes = nodes.slice(0, MAX);
    const keep = new Set(nodes.map((n) => n.id ?? ''));
    links = links.filter((l) => keep.has(l.from?.id ?? '') && keep.has(l.to?.id ?? ''));
    const ns: N[] = nodes.map((n) => ({ id: n.id ?? '', node: n, label: labelOf(n), ns: n.namespace ?? '', degree: 0 }));
    const byId = new Map(ns.map((n) => [n.id, n]));
    const ls: L[] = links.map((l) => ({ id: l.id ?? `${l.from?.id}-${l.type}-${l.to?.id}`, type: l.type ?? '', source: l.from?.id ?? '', target: l.to?.id ?? '' }));
    for (const l of ls) {
      byId.get(l.source as string)!.degree++;
      byId.get(l.target as string)!.degree++;
    }
    // deterministic start: nodes on a spiral by namespace
    ns.forEach((n, i) => {
      const a = i * 2.39996;
      const r = 12 * Math.sqrt(i + 1);
      n.x = r * Math.cos(a);
      n.y = r * Math.sin(a);
    });
    const sim = forceSimulation<N>(ns)
      .force('link', forceLink<N, L>(ls).id((d) => d.id).distance((l) => (String(l.type).endsWith('part_of') ? 40 : 70)).strength(0.6))
      .force('charge', forceManyBody().strength(-140).distanceMax(600))
      .force('center', forceCenter(0, 0))
      .force('x', forceX(0).strength(0.03))
      .force('y', forceY(0).strength(0.03))
      .force('collide', forceCollide<N>().radius((d) => radius(d) + 3))
      .stop();
    for (let i = 0; i < 260; i++) sim.tick();
    return { nodes: ns, links: ls, byId, truncated };
  });

  const radius = (n: N) => 4 + Math.min(10, Math.sqrt(n.degree) * 1.8);
  const PALETTE = ['#4c78a8', '#f58518', '#54a24b', '#b279a2', '#e45756', '#72b7b2', '#eeca3b', '#9d755d', '#ff9da6', '#bab0ac'];
  const colour = (ns: string) => PALETTE[Math.max(0, namespaces.indexOf(ns)) % PALETTE.length];

  // selection
  let selected = $state<string>('');
  let hovered = $state<string>('');
  const sel = $derived(view.byId.get(selected));
  const neighbours = $derived.by(() => {
    const out = new Set<string>();
    if (!selected) return out;
    for (const l of view.links) {
      const s = (l.source as N).id;
      const t = (l.target as N).id;
      if (s === selected) out.add(t);
      if (t === selected) out.add(s);
    }
    return out;
  });
  const selLinks = $derived(
    view.links
      .filter((l) => (l.source as N).id === selected || (l.target as N).id === selected)
      .map((l) => {
        const out = (l.source as N).id === selected;
        return { type: l.type, out, other: (out ? l.target : l.source) as N };
      })
      .sort((a, b) => a.type.localeCompare(b.type)),
  );

  // pan and zoom
  let cw = $state(900);
  let ch = $state(600);
  let scale = $state(1);
  let tx = $state(0);
  let ty = $state(0);
  $effect(() => {
    // fit on each new layout
    const xs = view.nodes.map((n) => n.x ?? 0);
    const ys = view.nodes.map((n) => n.y ?? 0);
    if (!xs.length) return;
    const w = Math.max(...xs) - Math.min(...xs) + 80;
    const h = Math.max(...ys) - Math.min(...ys) + 80;
    const k = Math.min(2, untrack(() => cw) / w, untrack(() => ch) / h);
    scale = k;
    tx = untrack(() => cw) / 2 - ((Math.max(...xs) + Math.min(...xs)) / 2) * k;
    ty = untrack(() => ch) / 2 - ((Math.max(...ys) + Math.min(...ys)) / 2) * k;
  });
  let drag: { x: number; y: number; tx: number; ty: number } | null = null;
  function wheel(e: WheelEvent) {
    e.preventDefault();
    const k = e.deltaY < 0 ? 1.15 : 1 / 1.15;
    const rect = (e.currentTarget as Element).getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;
    const ns = Math.min(8, Math.max(0.05, scale * k));
    tx = mx - ((mx - tx) * ns) / scale;
    ty = my - ((my - ty) * ns) / scale;
    scale = ns;
  }
  function down(e: PointerEvent) {
    if ((e.target as Element).closest('.gnode')) return;
    drag = { x: e.clientX, y: e.clientY, tx, ty };
    (e.currentTarget as Element).setPointerCapture(e.pointerId);
  }
  function move(e: PointerEvent) {
    if (!drag) return;
    tx = drag.tx + e.clientX - drag.x;
    ty = drag.ty + e.clientY - drag.y;
  }
  const showLabel = (n: N) => n.id === selected || n.id === hovered || neighbours.has(n.id) || scale > 1.4 || (view.nodes.length < 60 && scale > 0.7);
  const shortType = (t: string) => t.split('@').pop() ?? t;
</script>

<div class="editor-page explorer-page">
  <div class="bar">
    <h2>Graph explorer</h2>
    <div class="presets" role="tablist" aria-label="Presets">
      <button type="button" class="small" class:primary={preset === 'all'} onclick={() => applyPreset('all')}>Everything</button>
      <button type="button" class="small" class:primary={preset === 'organisation'} onclick={() => applyPreset('organisation')}>Organisation</button>
      <button type="button" class="small" class:primary={preset === 'ownership'} onclick={() => applyPreset('ownership')}>Organisation and what it owns</button>
    </div>
    <input type="search" placeholder="Find a node (key, name, type)…" bind:value={filter} aria-label="Find a node" />
    <button type="button" class="small" onclick={() => load()} disabled={loading}>{loading ? 'Loading…' : 'Reload'}</button>
  </div>
  <div class="ns">
    {#each namespaces as n (n)}
      <label><input type="checkbox" bind:checked={shown[n]} /> <i style="background: {colour(n)}"></i>{n}
        <span class="hint">{all.nodes.filter((x) => x.namespace === n).length}</span></label>
    {/each}
    <span class="hint">{view.nodes.length} nodes · {view.links.length} links{view.truncated ? ` (first ${MAX}: filter to see more)` : ''}{focusOn ? ' · focused' : ''}</span>
    {#if focusOn}<button type="button" class="link" onclick={() => (focusOn = null)}>show all</button>{/if}
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="body">
    <div class="canvas" role="presentation" bind:clientWidth={cw} bind:clientHeight={ch} onwheel={wheel} onpointerdown={down} onpointermove={move} onpointerup={() => (drag = null)}>
      <svg width="100%" height="100%" aria-label="Graph of every namespace">
        <g transform="translate({tx},{ty}) scale({scale})">
          {#each view.links as l (l.id)}
            {@const s = l.source as N}
            {@const t = l.target as N}
            <line
              class="glink"
              class:hot={s.id === selected || t.id === selected}
              class:org={l.type.endsWith('part_of')}
              x1={s.x}
              y1={s.y}
              x2={t.x}
              y2={t.y}
              stroke-width={1 / Math.sqrt(scale)}
            ><title>{s.label} {shortType(l.type)} {t.label}</title></line>
          {/each}
          {#each view.nodes as n (n.id)}
            <g
              class="gnode"
              class:dim={!!selected && n.id !== selected && !neighbours.has(n.id)}
              transform="translate({n.x},{n.y})"
              role="button"
              tabindex="-1"
              onclick={() => (selected = selected === n.id ? '' : n.id)}
              ondblclick={() => void openNode(n.node)}
              onpointerenter={() => (hovered = n.id)}
              onpointerleave={() => (hovered = '')}
              onkeydown={(e) => e.key === 'Enter' && void openNode(n.node)}
            >
              <circle r={radius(n)} fill={colour(n.ns)} class:sel={n.id === selected} />
              {#if showLabel(n)}
                <text x={radius(n) + 3} y="3" style="font-size: {11 / Math.max(0.6, Math.min(scale, 2))}px">{n.label}</text>
              {/if}
            </g>
          {/each}
        </g>
      </svg>
      {#if loading}<p class="empty overlay">Loading the graph…</p>{/if}
    </div>
    <aside class="side">
      {#if sel}
        <h4><i style="background: {colour(sel.ns)}"></i>{sel.label}</h4>
        <p class="hint mono">{sel.node.key} · {sel.node.type}</p>
        {#if sel.node.state}<p class="hint">state {sel.node.state}</p>{/if}
        <div class="row">
          <button type="button" class="small primary" onclick={() => void openNode(sel.node)}>Open</button>
          <button type="button" class="small" onclick={() => (focusOn = { id: sel.id, depth: 1 })}>Focus</button>
          <button type="button" class="small" onclick={() => (focusOn = { id: sel.id, depth: 2 })}>Focus ×2</button>
        </div>
        <ul class="nb">
          {#each selLinks as x, i (i)}
            <li>
              <span class="hint">{x.out ? '→' : '←'} {shortType(x.type)}</span>
              <button type="button" class="link" onclick={() => (selected = x.other.id)}>{x.other.label}</button>
              <span class="hint">{shortType(x.other.node.type ?? '')}</span>
            </li>
          {:else}
            <li class="hint">No link.</li>
          {/each}
        </ul>
      {:else}
        <p class="hint">
          Every namespace at the head of main, and the links between them. Click a node to see its neighbourhood, double-click
          to open it; wheel to zoom, drag to pan. "Organisation and what it owns" shows the units, the users, and the nodes
          they own or belong to.
        </p>
      {/if}
    </aside>
  </div>
</div>

<style>
  .explorer-page {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .bar {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .bar h2 {
    margin: 0 8px 0 0;
    font-size: 1.1em;
  }
  .bar input[type='search'] {
    flex: 1;
    min-width: 160px;
  }
  .presets {
    display: flex;
    gap: 4px;
  }
  .ns {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
    align-items: center;
    margin: 6px 0;
    font-size: 0.9em;
  }
  i {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    margin-right: 4px;
    vertical-align: middle;
  }
  .body {
    flex: 1;
    display: grid;
    grid-template-columns: minmax(0, 1fr) 300px;
    gap: 8px;
    min-height: 480px;
  }
  @media (max-width: 900px) {
    .body {
      grid-template-columns: 1fr;
    }
    .canvas {
      height: 60vh;
    }
  }
  .canvas {
    position: relative;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    overflow: hidden;
    cursor: grab;
    touch-action: none;
  }
  .overlay {
    position: absolute;
    top: 40%;
    width: 100%;
    text-align: center;
  }
  .glink {
    stroke: var(--muted);
    opacity: 0.35;
  }
  .glink.org {
    opacity: 0.7;
  }
  .glink.hot {
    stroke: var(--accent);
    opacity: 1;
  }
  .gnode {
    cursor: pointer;
    outline: none;
  }
  .gnode.dim {
    opacity: 0.2;
  }
  .gnode circle {
    stroke: var(--bg);
    stroke-width: 1;
  }
  .gnode circle.sel {
    stroke: var(--text);
    stroke-width: 2.5;
  }
  .gnode text {
    fill: var(--text);
    pointer-events: none;
    paint-order: stroke;
    stroke: var(--surface);
    stroke-width: 3px;
  }
  .side {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 8px 10px;
    overflow: auto;
    background: var(--bg);
  }
  .side h4 {
    margin: 0;
  }
  .nb {
    list-style: none;
    padding: 0;
    margin: 8px 0 0;
  }
  .nb li {
    margin: 2px 0;
  }
</style>
