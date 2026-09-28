<script lang="ts">
  // Branch graph of a node's versions, like `git log --graph`: one row per version (newest first), one lane per
  // branch (main on the left), an edge from each version to the version(s) it descends from: a curve near the
  // parent where a branch forks, near the child where a branch is merged back.
  import type { GraphNode } from '../api';
  import { MAIN_BRANCH } from '../namespace';

  let {
    versions,
    selected,
    onselect,
    describe,
  }: {
    /** the versions of the node, any order */
    versions: GraphNode[];
    selected?: number;
    onselect?: (version: number) => void;
    /** text of a row after the version and its branch (reason, state, change…) */
    describe: (v: GraphNode) => string;
  } = $props();

  const ROW = 30;
  const LANE = 18;
  const PAD = 12;
  const R = 5;
  const HUES = [28, 145, 340, 265, 175, 300, 100, 0, 200, 60];

  const branchOf = (v: GraphNode) => v.branch || MAIN_BRANCH;

  /** parents of a version (legacy versions without parents descend from the previous one, as pkg/graph) */
  const parentsOf = (v: GraphNode): number[] =>
    v.parents?.length ? v.parents : (v.version ?? 0) > 1 ? [(v.version ?? 0) - 1] : [];

  const rows = $derived([...versions].sort((a, b) => (b.version ?? 0) - (a.version ?? 0)));

  const rowOf = $derived(new Map(rows.map((v, i) => [v.version ?? 0, i])));

  // Lanes, reused as git does: main keeps the first; a branch occupies a lane from the row it is merged into (or its
  // head) down to the row it forks from, and takes the first lane free over that span.
  const lanes = $derived.by(() => {
    const span = new Map<string, [number, number]>();
    const extend = (b: string, r: number) => {
      const s = span.get(b);
      span.set(b, s ? [Math.min(s[0], r), Math.max(s[1], r)] : [r, r]);
    };
    for (const v of rows) {
      const b = branchOf(v);
      extend(b, rowOf.get(v.version ?? 0) ?? 0);
      for (const pv of parentsOf(v)) {
        const p = rows.find((x) => x.version === pv);
        const pr = rowOf.get(pv);
        if (!p || pr === undefined || branchOf(p) === b) continue;
        // the curves end in another lane: a fork holds the lane down to just above p, a merge up to just below v
        extend(b, pr - 0.5);
        extend(branchOf(p), (rowOf.get(v.version ?? 0) ?? 0) + 0.5);
      }
    }
    const lane = new Map<string, number>([[MAIN_BRANCH, 0]]);
    const used: [number, number][][] = [[]];
    const order = [...span.entries()].filter(([b]) => b !== MAIN_BRANCH).sort((a, b) => a[1][0] - b[1][0]);
    for (const [b, [top, bottom]] of order) {
      let l = 1;
      while (used[l]?.some(([t, bt]) => top <= bt && bottom >= t)) l++;
      (used[l] ??= []).push([top, bottom]);
      lane.set(b, l);
    }
    return lane;
  });
  const laneCount = $derived(Math.max(1, ...[...lanes.values()].map((l) => l + 1)));
  const branchIndex = $derived([...new Set([...rows].reverse().map(branchOf))].filter((b) => b !== MAIN_BRANCH));
  const laneOf = (b: string) => lanes.get(b) ?? 0;
  // colours follow the branch (not the lane): two branches sharing a lane stay distinct
  const colorOf = (b: string) =>
    b === MAIN_BRANCH ? 'var(--accent)' : `hsl(${HUES[Math.max(0, branchIndex.indexOf(b)) % HUES.length]} 62% 50%)`;

  const x = (lane: number) => PAD + lane * LANE;
  const y = (row: number) => ROW / 2 + row * ROW;

  /** the oldest version of each branch (where it forks) and the newest (its head) */
  const firsts = $derived.by(() => {
    const m = new Map<string, number>();
    for (const v of rows) m.set(branchOf(v), v.version ?? 0);
    return m;
  });
  const heads = $derived.by(() => {
    const m = new Map<string, number>();
    for (const v of [...rows].reverse()) m.set(branchOf(v), v.version ?? 0);
    return m;
  });

  interface Edge {
    key: string;
    d: string;
    color: string;
  }

  const edges = $derived.by<Edge[]>(() => {
    const out: Edge[] = [];
    const byVersion = new Map(rows.map((v) => [v.version ?? 0, v]));
    for (const v of rows) {
      const cv = v.version ?? 0;
      const cr = rowOf.get(cv) ?? 0;
      const cl = laneOf(branchOf(v));
      for (const pv of parentsOf(v)) {
        const p = byVersion.get(pv);
        const pr = rowOf.get(pv);
        if (!p || pr === undefined) continue;
        const pl = laneOf(branchOf(p));
        const [x1, y1, x2, y2] = [x(cl), y(cr), x(pl), y(pr)];
        let d: string;
        let color: string;
        if (pl === cl) {
          d = `M${x1},${y1} L${x2},${y2}`;
          color = colorOf(branchOf(v));
        } else if (firsts.get(branchOf(v)) === cv) {
          // the branch forks from its parent: leave the parent's row, then run up the branch's lane
          const yt = y2 - ROW * 0.8;
          const ym = (y2 + yt) / 2;
          d = `M${x2},${y2} C${x2},${ym} ${x1},${ym} ${x1},${yt} L${x1},${y1}`;
          color = colorOf(branchOf(v));
        } else {
          // merged back: run up the parent's lane, then join the child's row
          const yb = y1 + ROW * 0.8;
          const ym = (yb + y1) / 2;
          d = `M${x2},${y2} L${x2},${yb} C${x2},${ym} ${x1},${ym} ${x1},${y1}`;
          color = colorOf(branchOf(p));
        }
        out.push({ key: `${cv}<${pv}`, d, color });
      }
    }
    return out;
  });

  const width = $derived(PAD * 2 + (laneCount - 1) * LANE);
</script>

<div class="bgraph" style={`--gw:${width}px;--row:${ROW}px`}>
  <svg width={width} height={rows.length * ROW} aria-hidden="true">
    {#each edges as e (e.key)}<path d={e.d} style={`stroke:${e.color}`} />{/each}
    {#each rows as v, i (v.version)}
      {@const c = colorOf(branchOf(v))}
      {@const cx = x(laneOf(branchOf(v)))}
      {#if parentsOf(v).length > 1}
        <circle {cx} cy={y(i)} r={R + 2.5} class="ring" style={`stroke:${c}`} />
      {/if}
      <circle {cx} cy={y(i)} r={R} class:hollow={v.deleted} class:sel={selected === v.version} style={`stroke:${c};fill:${v.deleted ? 'var(--surface)' : c}`} />
    {/each}
  </svg>
  <ul aria-label="Versions by branch">
    {#each rows as v (v.version)}
      <li>
        <button type="button" class:sel={selected === v.version} aria-pressed={selected === v.version} onclick={() => onselect?.(v.version ?? 0)}>
          <strong>v{v.version}</strong>
          {#if heads.get(branchOf(v)) === v.version}
            <span class="ref" style={`--c:${colorOf(branchOf(v))}`}>{branchOf(v)}</span>
          {/if}
          {#if v.state}<span class="state">{v.state}</span>{/if}
          <span class="desc">{describe(v)}</span>
        </button>
      </li>
    {/each}
  </ul>
</div>

<style>
  .bgraph {
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
  button:hover {
    background: var(--hover);
  }
  button.sel {
    background: var(--accent-soft);
  }
  .ref {
    border: 1px solid var(--c);
    color: var(--c);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.78rem;
    font-family: var(--mono);
  }
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.78rem;
    font-family: var(--mono);
  }
  .desc {
    color: var(--muted);
    font-size: 0.88rem;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
