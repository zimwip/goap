<script lang="ts">
  // A lifecycle as a flow: states as boxes (current one filled, editable ones
  // dashed, final ones double-bordered), transitions as labelled arrows; the ones
  // available from the current state are highlighted and can be taken.
  import type { Lifecycle, LifecycleTransition } from '../api';

  let {
    lifecycle,
    current = '',
    stored = '',
    busy = false,
    disabled = false,
    onmove,
  }: {
    lifecycle: Lifecycle;
    /** state of the node (in the working change, when there is one) */
    current?: string;
    /** state of the stored version, shown when it differs */
    stored?: string;
    busy?: boolean;
    disabled?: boolean;
    onmove?: (t: LifecycleTransition) => void;
  } = $props();

  const W = 132;
  const H = 42;
  const GX = 96;
  const GY = 30;

  const states = $derived(lifecycle.states ?? []);
  const transitions = $derived(lifecycle.transitions ?? []);

  // rank = distance from the initial state along the transitions
  const rank = $derived.by(() => {
    const r = new Map<string, number>();
    const start = lifecycle.initial || states[0]?.name || '';
    if (start) r.set(start, 0);
    for (let changed = true, guard = 0; changed && guard < 50; guard++) {
      changed = false;
      for (const t of transitions) {
        if (r.has(t.from ?? '') && !r.has(t.to ?? '')) {
          r.set(t.to ?? '', (r.get(t.from ?? '') ?? 0) + 1);
          changed = true;
        }
      }
    }
    const max = Math.max(0, ...r.values());
    for (const s of states) if (!r.has(s.name ?? '')) r.set(s.name ?? '', max + 1);
    return r;
  });

  const boxes = $derived.by(() => {
    const byRank = new Map<number, string[]>();
    for (const s of states) byRank.set(rank.get(s.name ?? '') ?? 0, [...(byRank.get(rank.get(s.name ?? '') ?? 0) ?? []), s.name ?? '']);
    const out = new Map<string, { x: number; y: number }>();
    for (const [r, names] of byRank) names.forEach((n, i) => out.set(n, { x: r * (W + GX), y: (i - (names.length - 1) / 2) * (H + GY) }));
    return out;
  });

  interface Arrow {
    t: LifecycleTransition;
    d: string;
    head: string;
    lx: number;
    ly: number;
    avail: boolean;
  }

  const arrows = $derived.by<Arrow[]>(() => {
    const out: Arrow[] = [];
    let back = 0;
    const fanout = new Map<string, number>();
    for (const t of transitions) {
      const a = boxes.get(t.from ?? '');
      const b = boxes.get(t.to ?? '');
      if (!a || !b) continue;
      const pair = `${t.from}>${t.to}`;
      const k = fanout.get(pair) ?? 0;
      fanout.set(pair, k + 1);
      const forward = (rank.get(t.to ?? '') ?? 0) > (rank.get(t.from ?? '') ?? 0);
      let x1: number, y1: number, x2: number, y2: number, cx: number, cy: number;
      if (forward) {
        x1 = a.x + W / 2;
        y1 = a.y + k * 8;
        x2 = b.x - W / 2;
        y2 = b.y + k * 8;
        cx = (x1 + x2) / 2;
        cy = (y1 + y2) / 2 + (a.y === b.y ? -k * 10 : 0);
      } else {
        // backward: loop under the boxes
        back++;
        x1 = a.x;
        y1 = a.y + H / 2;
        x2 = b.x;
        y2 = b.y + H / 2;
        cx = (x1 + x2) / 2;
        cy = Math.max(a.y, b.y) + H / 2 + 34 + back * 14;
      }
      const dx = x2 - cx;
      const dy = y2 - cy;
      const len = Math.max(Math.hypot(dx, dy), 1);
      const ux = dx / len;
      const uy = dy / len;
      const bx = x2 - ux * 9;
      const by = y2 - uy * 9;
      out.push({
        t,
        d: `M${x1},${y1} Q${cx},${cy} ${bx},${by}`,
        head: `${x2},${y2} ${bx - uy * 4.5},${by + ux * 4.5} ${bx + uy * 4.5},${by - ux * 4.5}`,
        lx: 0.25 * x1 + 0.5 * cx + 0.25 * x2,
        ly: 0.25 * y1 + 0.5 * cy + 0.25 * y2,
        avail: t.from === current,
      });
    }
    return out;
  });

  const view = $derived.by(() => {
    const xs = [...boxes.values()].map((b) => b.x);
    const ys = [...boxes.values()].map((b) => b.y);
    if (!xs.length) return { x: 0, y: 0, w: 300, h: 100 };
    const x0 = Math.min(...xs) - W / 2 - 30;
    const x1 = Math.max(...xs) + W / 2 + 30;
    const y0 = Math.min(...ys) - H / 2 - 30;
    const y1 = Math.max(...ys) + H / 2 + 60 + arrows.filter((a) => (rank.get(a.t.to ?? '') ?? 0) <= (rank.get(a.t.from ?? '') ?? 0)).length * 14;
    return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
  });

  const stateOf = (name: string) => states.find((s) => s.name === name);
  const take = (t: LifecycleTransition) => {
    if (!disabled && !busy && t.from === current) onmove?.(t);
  };
</script>

<div class="lc">
  <div class="scroll">
    <svg viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`} style={`width:${Math.max(view.w, 360)}px;height:${Math.max(view.h, 120)}px`} role="img" aria-label="Lifecycle diagram">
      {#each arrows as a (`${a.t.name}|${a.t.from}|${a.t.to}`)}
        <g class="arrow" class:avail={a.avail}>
          <path d={a.d} />
          <polygon points={a.head} />
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <g
            class="label"
            transform={`translate(${a.lx} ${a.ly})`}
            role={a.avail && onmove ? 'button' : undefined}
            tabindex={a.avail && onmove && !disabled ? 0 : undefined}
            aria-label={a.avail && onmove ? `Take ${a.t.name}` : undefined}
            onclick={() => take(a.t)}
            onkeydown={(e) => (e.key === 'Enter' ? take(a.t) : undefined)}
          >
            <rect x={-(a.t.name?.length ?? 0) * 3.3 - 7} y="-9" width={(a.t.name?.length ?? 0) * 6.6 + 14} height="18" rx="9" />
            <text y="4" text-anchor="middle">{a.t.name}</text>
            <title>{a.t.name}: {a.t.from} → {a.t.to}{a.t.permission ? ` · needs ${a.t.permission}` : ''}{a.t.guard ? ` · guard ${a.t.guard}` : ''}</title>
          </g>
        </g>
      {/each}
      {#each states as s (s.name)}
        {@const p = boxes.get(s.name ?? '')}
        {#if p}
          <g class="state" class:current={s.name === current} class:editable={s.editable} class:final={s.final} transform={`translate(${p.x} ${p.y})`}>
            <rect x={-W / 2} y={-H / 2} width={W} height={H} rx="9" />
            {#if s.final}<rect x={-W / 2 + 4} y={-H / 2 + 4} width={W - 8} height={H - 8} rx="6" class="inner" />{/if}
            <text y={s.editable || s.name === stored ? -1 : 4} text-anchor="middle" class="n">{s.name}</text>
            {#if s.editable}<text y="12" text-anchor="middle" class="c">editable · working state</text>{:else if s.name === stored && stored !== current}<text y="12" text-anchor="middle" class="c">stored version</text>{/if}
            {#if s.name === lifecycle.initial}<circle cx={-W / 2 - 12} cy="0" r="4" class="init" />{/if}
            <title>{s.name}{s.description ? ` — ${s.description}` : ''}{s.editable ? ' (editable)' : ''}{s.final ? ' (final)' : ''}</title>
          </g>
        {/if}
      {/each}
    </svg>
  </div>

  <div class="legend hint">
    <span><i class="sw cur"></i> current</span>
    <span><i class="sw ed"></i> editable (working state: only held through a change)</span>
    <span><i class="sw fin"></i> final</span>
    <span><i class="sw ini"></i> initial</span>
  </div>

  <h4>Transitions</h4>
  <table>
    <thead><tr><th>Transition</th><th>From → to</th><th>Needs</th><th>Guard</th><th></th></tr></thead>
    <tbody>
      {#each transitions as t (`${t.name}|${t.from}|${t.to}`)}
        <tr class:avail={t.from === current}>
          <td><strong>{t.name}</strong></td>
          <td><span class="pill">{t.from}</span> → <span class="pill">{t.to}</span>{#if stateOf(t.to ?? '')?.editable}<span class="hint"> reopens</span>{/if}</td>
          <td class="hint">
            {t.permission ? `permission ${t.permission}` : 'node:transition'}
            {#if t.requiresAttributes?.length}· attributes {t.requiresAttributes.join(', ')}{/if}
            {#if t.requiresOutgoingLinks?.length}· links {t.requiresOutgoingLinks.join(', ')}{/if}
            {#if t.childrenStates?.length}· children in {t.childrenStates.join(' / ')}{/if}
          </td>
          <td>{#if t.guard}<code>{t.guard}</code>{/if}</td>
          <td class="act">
            {#if onmove && t.from === current}
              <button type="button" class="small primary" disabled={disabled || busy} onclick={() => take(t)}>{t.name} → {t.to}</button>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .scroll {
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 0.4rem;
  }
  svg {
    display: block;
    color: var(--text);
  }
  .state rect {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1.6;
  }
  .state .inner {
    fill: none;
  }
  .state.editable rect {
    stroke: var(--warn);
    stroke-dasharray: 5 3;
  }
  .state.final rect {
    stroke: var(--muted);
  }
  .state.current rect {
    fill: var(--accent);
    stroke: var(--accent);
  }
  .state.current .n,
  .state.current .c {
    fill: var(--accent-text);
  }
  .state .n {
    fill: currentColor;
    font-size: 13px;
    font-weight: 600;
    font-family: var(--mono);
  }
  .state .c {
    fill: var(--muted);
    font-size: 9px;
  }
  .init {
    fill: var(--muted);
  }
  .arrow path {
    fill: none;
    stroke: var(--muted);
    stroke-width: 1.4;
  }
  .arrow polygon {
    fill: var(--muted);
  }
  .arrow.avail path {
    stroke: var(--accent);
    stroke-width: 2.6;
  }
  .arrow.avail polygon {
    fill: var(--accent);
  }
  .label rect {
    fill: var(--surface);
    stroke: var(--border);
  }
  .arrow.avail .label rect {
    stroke: var(--accent);
    fill: var(--surface);
  }
  .arrow.avail .label {
    cursor: pointer;
  }
  .label text {
    fill: currentColor;
    font-size: 11px;
    pointer-events: none;
  }
  .legend {
    display: flex;
    gap: 1rem;
    flex-wrap: wrap;
    margin: 0.4rem 0;
  }
  .sw {
    display: inline-block;
    width: 12px;
    height: 12px;
    border-radius: 3px;
    border: 1.5px solid var(--border);
    vertical-align: -2px;
  }
  .sw.cur {
    background: var(--accent);
    border-color: var(--accent);
  }
  .sw.ed {
    border-style: dashed;
    border-color: var(--warn);
  }
  .sw.fin {
    border-color: var(--muted);
    box-shadow: inset 0 0 0 2px var(--surface), inset 0 0 0 3px var(--muted);
  }
  .sw.ini {
    border-radius: 50%;
    background: var(--muted);
    border-color: var(--muted);
    width: 8px;
    height: 8px;
  }
  h4 {
    margin: 0.8rem 0 0.3rem;
    font-size: 0.95rem;
  }
  tr.avail td {
    background: var(--hover);
  }
  .pill {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.85rem;
    font-family: var(--mono);
  }
  .act {
    text-align: right;
  }
  code {
    font-size: 0.8rem;
  }
</style>
