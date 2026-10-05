<script lang="ts">
  // Read-only diagram of a lifecycle: the states in a row, forward transitions as square rails above,
  // backward ones (dashed) below, each carrying its name as a pill.
  import type { LifecycleForm } from '../methodologyForm';

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

  /** colour of a state: initial, final, not landable or a plain landable one */
  function tone(s: { name: string; notLandable: boolean; final: boolean }): string {
    if (s.final) return 'var(--ok)';
    if (s.notLandable) return 'var(--warn)';
    if (s.name === lc.initial) return 'var(--accent)';
    return 'var(--info)';
  }

  interface Edge {
    i: number;
    name: string;
    from: number;
    to: number;
    span: number;
    x1: number;
    x2: number;
  }

  const layout = $derived.by(() => {
    const edges: Edge[] = [];
    lc.transitions.forEach((t, i) => {
      const from = index.get(t.from.trim());
      const to = index.get(t.to.trim());
      if (from === undefined || to === undefined || from === to) return;
      edges.push({ i, name: t.name.trim(), from, to, span: to - from, x1: cx(from), x2: cx(to) });
    });

    // several rails on one state edge: spread their attachment points so none overlap or cross
    const conns = new Map<string, { e: Edge; role: 'from' | 'to'; other: number }[]>();
    const add = (state: number, edge: string, e: Edge, role: 'from' | 'to', other: number) => {
      const k = `${state}:${edge}`;
      conns.set(k, [...(conns.get(k) ?? []), { e, role, other }]);
    };
    for (const e of edges) {
      const edge = e.span > 0 ? 'top' : 'bot';
      add(e.from, edge, e, 'from', e.to);
      add(e.to, edge, e, 'to', e.from);
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
        if (c.role === 'from') c.e.x1 = start + n * step;
        else c.e.x2 = start + n * step;
      });
    }

    const maxFwd = Math.max(0, ...edges.filter((e) => e.span > 0).map((e) => e.span));
    const maxBwd = Math.max(0, ...edges.filter((e) => e.span < 0).map((e) => -e.span));
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

  function rail(e: Edge, rowY: number) {
    const fwd = e.span > 0;
    const offset = BASE_CURVE + (Math.abs(e.span) - 1) * CURVE_STEP;
    const connY = fwd ? rowY - BOX_H / 2 : rowY + BOX_H / 2;
    const midY = fwd ? connY - offset : connY + offset;
    const midX = (e.x1 + e.x2) / 2;
    const hw = e.name ? Math.max(44, e.name.length * 6 + 18) / 2 : 0;
    const gap = hw ? hw + CHIP_GAP : 0;
    const s = fwd ? 1 : -1;
    // d1 leaves the source up to the chip, d2 goes on from the chip into the target (arrowhead)
    const d1 = `M ${e.x1},${connY} V ${midY + s * R} Q ${e.x1},${midY} ${e.x1 + s * R},${midY} H ${midX - s * gap}`;
    const d2 = `M ${midX + s * gap},${midY} H ${e.x2 - s * R} Q ${e.x2},${midY} ${e.x2},${midY + s * R} V ${connY}`;
    return { fwd, d1, d2, midX, midY, hw };
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

      {#each layout.edges as e (e.i)}
        {@const r = rail(e, layout.rowY)}
        {@const c = tone(states[e.to])}
        <g class="edge" style="--c: {c}">
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
        {@const flags = [here ? 'CURRENT' : '', s.name === lc.initial ? 'INITIAL' : '', s.notLandable ? 'NOT LANDABLE' : '', s.final ? 'FINAL' : ''].filter(Boolean).join(' · ')}
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
