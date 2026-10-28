<script lang="ts">
  // Read-only diagram of a lifecycle: the states in a row, forward transitions as square rails above,
  // backward ones (dashed) below, each carrying its name as a pill. Transitions sharing a name and a target are one
  // rail: their sources merge into a trunk that carries the name once (`lifecycleBundles.ts`).
  import type { LifecycleForm } from '../methodologyForm';
  import { bundleLevels, bundleTransitions, labelX, type Bundle } from '../lifecycleBundles';

  let { lc, current = '' }: { lc: LifecycleForm; /** the state something is in: drawn emphasised */ current?: string } = $props();

  const BOX_W = 110;
  const BOX_H = 36;
  const H_GAP = 72;
  const PAD = 28;
  const BASE_CURVE = 46;
  const CURVE_STEP = 32;
  const R = 10;
  const BTN_H = 16;
  const CHIP_GAP = 4;
  const SPREAD = BOX_W * 0.6;

  const states = $derived(lc.states.map((s) => ({ ...s, name: s.name.trim() })).filter((s) => s.name));
  const index = $derived(new Map(states.map((s, i) => [s.name, i])));
  const cx = (i: number) => PAD + i * (BOX_W + H_GAP) + BOX_W / 2;

  /** colour of a state: initial, final, not committable or a plain committable one */
  function tone(s: { name: string; notCommittable: boolean; final: boolean }): string {
    if (s.final) return 'var(--ok)';
    if (s.notCommittable) return 'var(--warn)';
    if (s.name === lc.initial) return 'var(--accent)';
    return 'var(--info)';
  }

  /** a bundle placed: the x where each source leaves its box (same order as `from`) and where the trunk enters the target */
  interface Edge extends Bundle {
    xs: number[];
    x2: number;
    /** level of its rail, 1 nearest the boxes */
    level: number;
  }

  const layout = $derived.by(() => {
    const bundles = bundleTransitions(lc.transitions, index);
    const levels = bundleLevels(bundles);
    const edges: Edge[] = bundles.map((b) => ({ ...b, xs: b.from.map(cx), x2: cx(b.to), level: levels.get(b.key) ?? 1 }));

    // several rails on one state edge: spread their attachment points so none overlap or cross
    type Conn = { e: Edge; role: 'from' | 'to'; n: number; other: number };
    const conns = new Map<string, Conn[]>();
    const add = (state: number, c: Conn) => {
      const k = `${state}:${c.e.dir > 0 ? 'top' : 'bot'}`;
      conns.set(k, [...(conns.get(k) ?? []), c]);
    };
    for (const e of edges) {
      e.from.forEach((f, n) => add(f, { e, role: 'from', n, other: e.to }));
      add(e.to, { e, role: 'to', n: 0, other: e.from[e.from.length - 1] });
    }
    for (const [k, items] of conns) {
      if (items.length < 2) continue;
      const [sIdx, edge] = k.split(':');
      const s = Number(sIdx);
      const dist = (c: { other: number }) => Math.abs(c.other - s);
      const inc = items.filter((c) => c.role === 'to');
      const out = items.filter((c) => c.role === 'from');
      let ordered;
      if (edge === 'top') {
        inc.sort((a, b) => dist(a) - dist(b));
        out.sort((a, b) => dist(b) - dist(a));
        ordered = [...inc, ...out];
      } else {
        out.sort((a, b) => dist(a) - dist(b));
        inc.sort((a, b) => dist(b) - dist(a));
        ordered = [...out, ...inc];
      }
      const start = cx(s) - SPREAD / 2;
      const step = SPREAD / (ordered.length - 1);
      ordered.forEach((c, n) => {
        if (c.role === 'from') c.e.xs[c.n] = start + n * step;
        else c.e.x2 = start + n * step;
      });
    }

    const maxFwd = Math.max(0, ...edges.filter((e) => e.dir > 0).map((e) => e.level));
    const maxBwd = Math.max(0, ...edges.filter((e) => e.dir < 0).map((e) => e.level));
    const top = maxFwd ? BASE_CURVE + (maxFwd - 1) * CURVE_STEP + BTN_H + 16 : 20;
    const bot = maxBwd ? BASE_CURVE + (maxBwd - 1) * CURVE_STEP + BTN_H + 28 : 30;
    const rowY = PAD + top + BOX_H / 2;
    return {
      edges,
      rowY,
      width: PAD * 2 + states.length * (BOX_W + H_GAP) - H_GAP,
      height: rowY + BOX_H / 2 + bot + PAD,
    };
  });

  /**
   * The paths of a bundle: the trunk leaves the farthest source, runs at its level and drops into the
   * target (d1 up to the chip, d2 from the chip on, arrowhead); each nearer source rises and curves into the trunk
   * towards the target (branches, no arrowhead).
   */
  function rail(e: Edge, rowY: number) {
    const fwd = e.dir > 0;
    const offset = BASE_CURVE + (e.level - 1) * CURVE_STEP;
    const connY = fwd ? rowY - BOX_H / 2 : rowY + BOX_H / 2;
    const midY = fwd ? connY - offset : connY + offset;
    const x1 = e.xs[e.xs.length - 1];
    const midX = labelX(e.xs, e.x2);
    const hw = e.name ? Math.max(44, e.name.length * 6 + 18) / 2 : 0;
    const gap = hw ? hw + CHIP_GAP : 0;
    const s = fwd ? 1 : -1;
    const v = fwd ? -1 : 1;
    const d1 = `M ${x1},${connY} V ${midY - v * R} Q ${x1},${midY} ${x1 + s * R},${midY} H ${midX - s * gap}`;
    const d2 = `M ${midX + s * gap},${midY} H ${e.x2 - s * R} Q ${e.x2},${midY} ${e.x2},${midY - v * R} V ${connY}`;
    const branches = e.xs.slice(0, -1).map((x) => `M ${x},${connY} V ${midY - v * R} Q ${x},${midY} ${x + s * R},${midY}`);
    return { fwd, d1, d2, branches, midX, midY, hw };
  }
</script>

{#if !states.length}
  <p class="empty">No states yet.</p>
{:else}
  <div class="lcd" role="img" aria-label="Lifecycle {lc.name}">
    <svg width={layout.width} height={layout.height} viewBox="0 0 {layout.width} {layout.height}" style="overflow: visible">
      <defs>
        <marker id="lcp-arr" markerWidth="7" markerHeight="7" refX="5" refY="3.5" orient="auto">
          <path d="M0,0.5 L0,6.5 L6,3.5 z" fill="context-stroke" opacity="0.8" />
        </marker>
      </defs>

      {#each layout.edges as e (e.key)}
        {@const r = rail(e, layout.rowY)}
        {@const c = tone(states[e.to])}
        <g class="edge" style="--c: {c}">
          <title>{e.name}: {e.from.map((f) => states[f].name).join(', ')} → {states[e.to].name}</title>
          {#each r.branches as d, n (n)}
            <path {d} fill="none" stroke={c} stroke-width="1.5" stroke-dasharray={r.fwd ? 'none' : '4,3'} opacity="0.75" />
          {/each}
          <path d={r.d1} fill="none" stroke={c} stroke-width="1.5" stroke-dasharray={r.fwd ? 'none' : '4,3'} opacity="0.75" />
          <path d={r.d2} fill="none" stroke={c} stroke-width="1.5" stroke-dasharray={r.fwd ? 'none' : '4,3'} opacity="0.75" marker-end="url(#lcp-arr)" />
          {#if e.name}
            <rect class="pill" x={r.midX - r.hw} y={r.midY - BTN_H / 2} width={r.hw * 2} height={BTN_H} rx="8" />
            <text class="pilltext" x={r.midX} y={r.midY + 3.5} text-anchor="middle">{e.name}</text>
          {/if}
        </g>
      {/each}

      {#each states as s, i (i)}
        {@const c = tone(s)}
        {@const here = !!current && s.name === current}
        {@const flags = [here ? 'CURRENT' : '', s.name === lc.initial ? 'INITIAL' : '', s.notCommittable ? 'NOT LANDABLE' : '', s.final ? 'FINAL' : ''].filter(Boolean).join(' · ')}
        <g>
          <title>{s.description || s.name}</title>
          <rect class="state" class:here x={cx(i) - BOX_W / 2} y={layout.rowY - BOX_H / 2} width={BOX_W} height={BOX_H} rx="6" style="--c: {c}" />
          <text class="sname" x={cx(i)} y={layout.rowY + (flags ? 1 : 4)} text-anchor="middle" style="fill: {c}">{s.name}</text>
          {#if flags}<text class="sflags" x={cx(i)} y={layout.rowY + 13} text-anchor="middle" style="fill: {c}">{flags}</text>{/if}
        </g>
      {/each}
    </svg>
  </div>
{/if}

<style>
  .lcd {
    overflow-x: auto;
    padding: 0.4rem 0;
  }
  .state {
    fill: color-mix(in srgb, var(--c) 14%, transparent);
    stroke: var(--c);
    stroke-width: 1.5;
  }
  .state.here {
    fill: color-mix(in srgb, var(--c) 32%, transparent);
    stroke-width: 3;
  }
  .sname {
    font-size: 11px;
    font-weight: 700;
    font-family: var(--mono);
  }
  .sflags {
    font-size: 7px;
    opacity: 0.8;
  }
  .pill {
    fill: color-mix(in srgb, var(--c) 12%, var(--surface));
    stroke: color-mix(in srgb, var(--c) 45%, transparent);
  }
  .pilltext {
    font-size: 9px;
    font-weight: 700;
    fill: var(--c);
    user-select: none;
  }
  .empty {
    color: var(--muted);
    font-size: 0.9em;
  }
</style>
