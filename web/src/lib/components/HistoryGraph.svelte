<script module lang="ts">
  /** an edge to a parent entry: what a row descends from is a dot, what changed it to get there is the edge */
  export interface LaneEdge {
    /** the parent entry's id */
    id: string;
    /** shown on the edge (e.g. the change that produced this transition), as a native SVG tooltip */
    label?: string;
    /** draws this one edge dashed, regardless of either endpoint's own `ephemeral` */
    dashed?: boolean;
  }

  export interface LaneEntry<T = unknown> {
    /** stable, unique key */
    id: string;
    /** the entries this one descends from (empty: a root) */
    parents: LaneEdge[];
    /** branch / lane key */
    lane: string;
    /** hollow dot */
    deleted?: boolean;
    /** scratch work (e.g. a change's own throwaway branch, not a branch a person opened): drawn dashed */
    ephemeral?: boolean;
    /** the caller's own item, handed back to `row` */
    item: T;
  }
</script>

<script lang="ts" generics="T">
  // Agnostic history graph, like `git log --graph`: one row per entry (caller decides the order, newest first),
  // one lane per branch (the main lane on the left), an edge from each entry to the entry(-ies) it descends from: a
  // curve near the parent where a lane forks, near the child where a lane is merged back.
  //
  // Domain-agnostic: the caller maps its own items (node versions, baselines, anything with a lineage) to
  // `LaneEntry<T>` and supplies a `row` snippet to render each row's content — the component only draws the graph
  // and the row's clickable shell.
  import type { Snippet } from 'svelte';

  let {
    entries,
    selected,
    mainLane = 'main',
    rowHeight = 30,
    wrap = false,
    onselect,
    onopen,
    oncontextmenu,
    row,
  }: {
    /** rows, newest first */
    entries: LaneEntry<T>[];
    selected?: string;
    /** the lane drawn in the accent color and kept in lane 0 */
    mainLane?: string;
    /** row height in px; taller than the default 30 to let `row` lay out multiple lines */
    rowHeight?: number;
    /** let `row`'s content wrap onto more than one line instead of eliding past the column width */
    wrap?: boolean;
    /** single click / Enter: view this entry */
    onselect?: (id: string) => void;
    /** double click: open this entry (its own tab, pinned) */
    onopen?: (id: string) => void;
    /** right click */
    oncontextmenu?: (id: string, e: MouseEvent) => void;
    /** a row's content; `head` is true if this is the newest entry of its lane, `color` its lane's color */
    row: Snippet<[{ entry: LaneEntry<T>; head: boolean; color: string }]>;
  } = $props();

  const ROW = $derived(rowHeight);
  const LANE = 18;
  const PAD = 12;
  const R = 5;
  const HUES = [28, 145, 340, 265, 175, 300, 100, 0, 200, 60];

  const rowOf = $derived(new Map(entries.map((e, i) => [e.id, i])));

  // Lanes, reused as git does: the main lane keeps the first; a branch occupies a lane from the row it is merged
  // into (or its head) down to the row it forks from, and takes the first lane free over that span.
  const lanes = $derived.by(() => {
    const span = new Map<string, [number, number]>();
    const extend = (l: string, r: number) => {
      const s = span.get(l);
      span.set(l, s ? [Math.min(s[0], r), Math.max(s[1], r)] : [r, r]);
    };
    for (const e of entries) {
      extend(e.lane, rowOf.get(e.id) ?? 0);
      for (const { id: pid } of e.parents) {
        const p = entries.find((x) => x.id === pid);
        const pr = rowOf.get(pid);
        if (!p || pr === undefined || p.lane === e.lane) continue;
        // the curves end in another lane: a fork holds the lane down to just above p, a merge up to just below e
        extend(e.lane, pr - 0.5);
        extend(p.lane, (rowOf.get(e.id) ?? 0) + 0.5);
      }
    }
    const lane = new Map<string, number>([[mainLane, 0]]);
    const used: [number, number][][] = [[]];
    const order = [...span.entries()].filter(([l]) => l !== mainLane).sort((a, b) => a[1][0] - b[1][0]);
    for (const [l, [top, bottom]] of order) {
      let i = 1;
      while (used[i]?.some(([t, bt]) => top <= bt && bottom >= t)) i++;
      (used[i] ??= []).push([top, bottom]);
      lane.set(l, i);
    }
    return lane;
  });
  const laneCount = $derived(Math.max(1, ...[...lanes.values()].map((l) => l + 1)));
  const laneIndex = $derived([...new Set([...entries].reverse().map((e) => e.lane))].filter((l) => l !== mainLane));
  const laneOf = (l: string) => lanes.get(l) ?? 0;
  // colours follow the lane (not its position): two lanes sharing a position stay distinct
  const colorOf = (l: string) => (l === mainLane ? 'var(--accent)' : `hsl(${HUES[Math.max(0, laneIndex.indexOf(l)) % HUES.length]} 62% 50%)`);

  const x = (lane: number) => PAD + lane * LANE;
  const y = (row: number) => ROW / 2 + row * ROW;

  /** the oldest entry of each lane (where it forks) and the newest (its head) */
  const firsts = $derived.by(() => {
    const m = new Map<string, string>();
    for (const e of entries) m.set(e.lane, e.id);
    return m;
  });
  const heads = $derived.by(() => {
    const m = new Map<string, string>();
    for (const e of [...entries].reverse()) m.set(e.lane, e.id);
    return m;
  });

  interface Edge {
    key: string;
    d: string;
    color: string;
    /** the change that produced this transition, if the caller gave one */
    label?: string;
    /** scratch work on either end: drawn dashed */
    ephemeral?: boolean;
  }

  const edges = $derived.by<Edge[]>(() => {
    const out: Edge[] = [];
    const byId = new Map(entries.map((e) => [e.id, e]));
    for (const e of entries) {
      const cr = rowOf.get(e.id) ?? 0;
      const cl = laneOf(e.lane);
      for (const { id: pid, label, dashed } of e.parents) {
        const p = byId.get(pid);
        const pr = rowOf.get(pid);
        if (!p || pr === undefined) continue;
        const ephemeral = e.ephemeral || p.ephemeral || dashed;
        const pl = laneOf(p.lane);
        const [x1, y1, x2, y2] = [x(cl), y(cr), x(pl), y(pr)];
        let d: string;
        let color: string;
        if (pl === cl) {
          d = `M${x1},${y1} L${x2},${y2}`;
          color = colorOf(e.lane);
        } else if (firsts.get(e.lane) === e.id) {
          // the lane forks from its parent: leave the parent's row, then run up the lane's own position
          const yt = y2 - ROW * 0.8;
          const ym = (y2 + yt) / 2;
          d = `M${x2},${y2} C${x2},${ym} ${x1},${ym} ${x1},${yt} L${x1},${y1}`;
          color = colorOf(e.lane);
        } else {
          // merged back: run up the parent's position, then join the child's row
          const yb = y1 + ROW * 0.8;
          const ym = (yb + y1) / 2;
          d = `M${x2},${y2} L${x2},${yb} C${x2},${ym} ${x1},${ym} ${x1},${y1}`;
          color = colorOf(p.lane);
        }
        out.push({ key: `${e.id}<${pid}`, d, color, label, ephemeral });
      }
    }
    return out;
  });

  const width = $derived(PAD * 2 + (laneCount - 1) * LANE);
</script>

<div class="hgraph" style={`--gw:${width}px;--row:${ROW}px`}>
  <svg width={width} height={entries.length * ROW} aria-hidden="true">
    {#each edges as e (e.key)}<path d={e.d} class:ephemeral={e.ephemeral} style={`stroke:${e.color}`}>{#if e.label}<title>{e.label}</title>{/if}</path>{/each}
    {#each entries as e, i (e.id)}
      {@const c = colorOf(e.lane)}
      {@const cx = x(laneOf(e.lane))}
      {#if e.parents.length > 1}
        <circle {cx} cy={y(i)} r={R + 2.5} class="ring" style={`stroke:${c}`} />
      {/if}
      <circle {cx} cy={y(i)} r={R} class:hollow={e.deleted} class:ephemeral={e.ephemeral} class:sel={selected === e.id} style={`stroke:${c};fill:${e.deleted ? 'var(--surface)' : c}`} />
    {/each}
  </svg>
  <ul aria-label="History">
    {#each entries as e (e.id)}
      <li>
        <button
          type="button"
          class:sel={selected === e.id}
          class:wrap
          aria-pressed={selected === e.id}
          onclick={() => onselect?.(e.id)}
          ondblclick={() => onopen?.(e.id)}
          oncontextmenu={(ev) => {
            if (!oncontextmenu) return;
            ev.preventDefault();
            oncontextmenu(e.id, ev);
          }}
        >
          {@render row({ entry: e, head: heads.get(e.lane) === e.id, color: colorOf(e.lane) })}
        </button>
      </li>
    {/each}
  </ul>
</div>

<style>
  .hgraph {
    position: relative;
    overflow-x: auto;
  }
  svg {
    position: absolute;
    left: 0;
    top: 0;
  }
  path {
    fill: none;
    stroke-width: 2;
  }
  circle {
    stroke-width: 2;
  }
  circle.hollow {
    stroke-dasharray: 2 2;
  }
  circle.ephemeral,
  path.ephemeral {
    stroke-dasharray: 4 3;
    opacity: 0.7;
  }
  circle.sel {
    stroke: var(--text) !important;
    stroke-width: 2.5;
  }
  circle.ring {
    fill: none;
    stroke-width: 1.5;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    height: var(--row);
  }
  button {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    width: 100%;
    height: 100%;
    padding: 0 0.5rem 0 calc(var(--gw) + 0.3rem);
    border: none;
    border-radius: 4px;
    background: none;
    color: inherit;
    text-align: left;
    cursor: pointer;
    white-space: nowrap;
    min-width: max-content;
    font-weight: normal;
  }
  button.wrap {
    align-items: flex-start;
    white-space: normal;
    min-width: 0;
    padding-top: 3px;
    padding-bottom: 3px;
  }
  button:hover {
    background: var(--hover);
  }
  button.sel {
    background: var(--accent-soft);
  }
</style>
