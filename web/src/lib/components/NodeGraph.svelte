<script lang="ts">
  // Graph centered on a node: parents on the left, children on the right,
  // one or two levels deep. Click a node to recenter, double-click to open it.
  import type { GraphIndex } from '../graphIndex';

  let {
    index,
    center,
    onrecenter,
    onopen,
  }: {
    index: GraphIndex;
    center: string;
    onrecenter: (id: string) => void;
    onopen: (id: string) => void;
  } = $props();

  let depth = $state<1 | 2>(1);

  const W = 168;
  const H = 34;
  const GX = 90;
  const GY = 16;
  const CAP = 14;

  interface Box {
    id: string;
    col: number;
    x: number;
    y: number;
    more?: number;
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
    if (depth === 2) {
      expand(-1, 'in', -1);
      expand(1, 'out', 1);
    }
    return { placed, byCol };
  });

  const boxes = $derived.by<Box[]>(() => {
    const out: Box[] = [];
    const maxRows = Math.max(1, ...[...cols.byCol.values()].map((ids) => Math.min(ids.length, CAP + 1)));
    for (const [col, ids] of cols.byCol) {
      const shown = ids.filter((id) => index.nodes.has(id)).sort((a, b) => (index.nodes.get(a)?.key ?? '').localeCompare(index.nodes.get(b)?.key ?? ''));
      const list = shown.slice(0, CAP);
      const total = list.length + (shown.length > CAP ? 1 : 0);
      list.forEach((id, i) => out.push({ id, col, x: col * (W + GX), y: (i - (total - 1) / 2) * (H + GY) }));
      if (shown.length > CAP) out.push({ id: `…${col}`, col, x: col * (W + GX), y: (list.length - (total - 1) / 2) * (H + GY), more: shown.length - CAP });
    }
    void maxRows;
    return out;
  });

  const boxOf = $derived(new Map(boxes.map((b) => [b.id, b])));

  const HUES = [212, 28, 145, 340, 265, 175, 50, 300, 100, 0];
  const linkTypes = $derived([...new Set(index.list.length ? [...index.out.values()].flat().map((l) => l.type ?? '') : [])].sort());
  const colorOf = (t: string | undefined) => `hsl(${HUES[Math.max(0, linkTypes.indexOf(t ?? '')) % HUES.length]} 60% 55%)`;

  interface Edge {
    key: string;
    d: string;
    head: string;
    lx: number;
    ly: number;
    type: string;
    color: string;
    touchesCenter: boolean;
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
        const cy = same ? (y1 + y2) / 2 : (y1 + y2) / 2;
        const dx = x2 - cx;
        const dy = y2 - cy;
        const len = Math.max(Math.hypot(dx, dy), 1);
        const ux = dx / len;
        const uy = dy / len;
        const bx = x2 - ux * 9;
        const by = y2 - uy * 9;
        out.push({
          key,
          d: `M${x1},${y1} Q${cx},${cy} ${bx},${by}`,
          head: `${x2},${y2} ${bx - uy * 4},${by + ux * 4} ${bx + uy * 4},${by - ux * 4}`,
          lx: 0.25 * x1 + 0.5 * cx + 0.25 * x2,
          ly: 0.25 * y1 + 0.5 * cy + 0.25 * y2,
          type: l.type ?? '',
          color: colorOf(l.type),
          touchesCenter: b.id === center || t.id === center,
        });
      }
    }
    return out;
  });

  const view = $derived.by(() => {
    if (!boxes.length) return { x: 0, y: 0, w: 400, h: 120 };
    const x0 = Math.min(...boxes.map((b) => b.x)) - W / 2 - 24;
    const x1 = Math.max(...boxes.map((b) => b.x)) + W / 2 + 24;
    const y0 = Math.min(...boxes.map((b) => b.y)) - H / 2 - 24;
    const y1 = Math.max(...boxes.map((b) => b.y)) + H / 2 + 24;
    return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
  });

  const trunc = (s: string, n = 22) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);
</script>

<div class="graph">
  <div class="tools" role="group" aria-label="Graph depth">
    <span class="hint">Levels</span>
    <button type="button" class="small" class:primary={depth === 1} onclick={() => (depth = 1)}>1</button>
    <button type="button" class="small" class:primary={depth === 2} onclick={() => (depth = 2)}>2</button>
    <span class="hint legend">parents ← node → children</span>
  </div>
  <div class="scroll">
    <svg viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`} style={`width:${Math.max(view.w, 320)}px;height:${Math.max(view.h, 120)}px`} role="img" aria-label="Graph of the node and its neighbours">
      {#each edges as e (e.key)}
        <g class="edge" class:dim={!e.touchesCenter} style={`--c:${e.color}`}>
          <path d={e.d} />
          <polygon points={e.head} />
          <g transform={`translate(${e.lx} ${e.ly})`}>
            <rect x={-(e.type.length * 3.2 + 6)} y="-8" width={e.type.length * 6.4 + 12} height="16" rx="8" />
            <text y="3.5" text-anchor="middle">{e.type}</text>
          </g>
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
            class:center={b.id === center}
            transform={`translate(${b.x} ${b.y})`}
            role="button"
            tabindex="0"
            aria-label={`${n?.key} (${n?.type})`}
            onclick={() => onrecenter(b.id)}
            ondblclick={() => onopen(b.id)}
            onkeydown={(e) => (e.key === 'Enter' ? onrecenter(b.id) : undefined)}
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
  .edge.dim {
    opacity: 0.45;
  }
  .more text {
    fill: var(--muted);
    font-size: 11px;
  }
</style>
