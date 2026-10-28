// Transitions of a lifecycle drawn as bundles: the transitions sharing a name, a target and a direction (forward or
// backward along the row of states) are one rail, its sources merging into one trunk that carries the name once.

export interface BundleTransition {
  name: string;
  from: string;
  to: string;
}

export interface Bundle {
  /** index of the first transition of the bundle, a stable key */
  key: number;
  name: string;
  to: number;
  /** indexes of the source states, nearest to the target first */
  from: number[];
  /** +1 forward (target to the right), -1 backward */
  dir: 1 | -1;
  /** distance in states from the farthest source to the target */
  span: number;
}

/** bundles of the transitions, given the position of each state in the row; self and unknown transitions are left out */
export function bundleTransitions(transitions: BundleTransition[], index: Map<string, number>): Bundle[] {
  const out: Bundle[] = [];
  const byKey = new Map<string, Bundle>();
  transitions.forEach((t, i) => {
    const from = index.get(t.from.trim());
    const to = index.get(t.to.trim());
    if (from === undefined || to === undefined || from === to) return;
    const name = t.name.trim();
    const dir = to > from ? 1 : -1;
    const k = `${name}\u0000${to}\u0000${dir}`;
    let b = byKey.get(k);
    if (!b) {
      b = { key: i, name, to, from: [], dir, span: 0 };
      byKey.set(k, b);
      out.push(b);
    }
    if (!b.from.includes(from)) b.from.push(from);
  });
  for (const b of out) {
    b.from.sort((x, y) => Math.abs(x - b.to) - Math.abs(y - b.to));
    b.span = Math.abs(b.from[b.from.length - 1] - b.to);
  }
  return out;
}

/**
 * Where the label of a trunk goes: the middle of the widest stretch between two of its junctions (the sources' x
 * and the target's x), so that no branch joins under the label. For one source, the middle of the rail.
 */
export function labelX(sourceXs: number[], targetX: number): number {
  const xs = [...sourceXs, targetX].sort((a, b) => a - b);
  let best = 0;
  let at = (xs[0] + xs[xs.length - 1]) / 2;
  for (let i = 1; i < xs.length; i++) {
    const w = xs[i] - xs[i - 1];
    if (w > best) {
      best = w;
      at = (xs[i] + xs[i - 1]) / 2;
    }
  }
  return at;
}

/**
 * The level of each bundle's rail on its side of the row (1 nearest the boxes): one above every shorter rail of the
 * same side whose stretch overlaps its own (touching at a state does not count), so a rail passes just over those it
 * spans and two rails never run along the same stretch.
 */
export function bundleLevels(bundles: Bundle[]): Map<number, number> {
  const range = (b: Bundle) => {
    const far = b.from[b.from.length - 1];
    return [Math.min(far, b.to), Math.max(far, b.to)];
  };
  const order = [...bundles].sort((a, b) => a.span - b.span || range(a)[0] - range(b)[0]);
  const placed: Bundle[] = [];
  const level = new Map<number, number>();
  for (const b of order) {
    const [lo, hi] = range(b);
    let l = 1;
    for (const p of placed) {
      if (p.dir !== b.dir) continue;
      const [plo, phi] = range(p);
      if (plo < hi && lo < phi) l = Math.max(l, (level.get(p.key) ?? 1) + 1);
    }
    level.set(b.key, l);
    placed.push(b);
  }
  return level;
}
