// Force-directed layout of an ontology (node types, link types, inheritance).
// Deterministic (no randomness): the same domain always gets the same picture.

export interface LNode {
  id: string;
  w: number;
  h: number;
  x: number;
  y: number;
}

export interface LEdge {
  a: string;
  b: string;
  /** inheritance edges (a extends b) push the subtype below its parent */
  kind: 'link' | 'extends';
}

/** Places the nodes (mutates x and y), centered on the origin. */
export function layoutGraph(nodes: LNode[], edges: LEdge[]): void {
  const n = nodes.length;
  if (n === 0) return;
  if (n === 1) {
    nodes[0].x = 0;
    nodes[0].y = 0;
    return;
  }
  const byId = new Map(nodes.map((v) => [v.id, v]));
  const radius = Math.sqrt(n) * 80;
  nodes.forEach((v, i) => {
    const a = (2 * Math.PI * i) / n;
    v.x = Math.cos(a) * radius;
    v.y = Math.sin(a) * radius;
  });
  const es = edges.map((e) => ({ ...e, u: byId.get(e.a), v: byId.get(e.b) })).filter((e) => e.u && e.v && e.u !== e.v);
  const K = 135;
  let temp = radius / 2;
  for (let iter = 0; iter < 400; iter++) {
    const dx = new Map<LNode, number>();
    const dy = new Map<LNode, number>();
    for (const v of nodes) {
      dx.set(v, 0);
      dy.set(v, 0);
    }
    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        const u = nodes[i];
        const v = nodes[j];
        let x = u.x - v.x;
        let y = u.y - v.y;
        let d = Math.hypot(x, y);
        if (d < 0.01) {
          x = 0.01 * (i + 1);
          y = 0.01 * (j + 1);
          d = Math.hypot(x, y);
        }
        const f = (K * K) / d;
        dx.set(u, dx.get(u)! + (x / d) * f);
        dy.set(u, dy.get(u)! + (y / d) * f);
        dx.set(v, dx.get(v)! - (x / d) * f);
        dy.set(v, dy.get(v)! - (y / d) * f);
      }
    }
    for (const e of es) {
      const u = e.u!;
      const v = e.v!;
      const x = u.x - v.x;
      const y = u.y - v.y;
      const d = Math.max(Math.hypot(x, y), 0.01);
      const ideal = e.kind === 'extends' ? K * 0.6 : K;
      const f = ((d - ideal) * d) / K;
      dx.set(u, dx.get(u)! - (x / d) * f);
      dy.set(u, dy.get(u)! - (y / d) * f);
      dx.set(v, dx.get(v)! + (x / d) * f);
      dy.set(v, dy.get(v)! + (y / d) * f);
      if (e.kind === 'extends') {
        // a extends b: the subtype (a) sits below its parent (b)
        const gap = u.y - v.y - 95;
        if (gap < 0) {
          dy.set(u, dy.get(u)! - gap * 0.9);
          dy.set(v, dy.get(v)! + gap * 0.9);
        }
      }
    }
    for (const v of nodes) {
      dx.set(v, dx.get(v)! - v.x * 0.05);
      dy.set(v, dy.get(v)! - v.y * 0.05);
      const d = Math.max(Math.hypot(dx.get(v)!, dy.get(v)!), 0.01);
      const m = Math.min(d, temp);
      v.x += (dx.get(v)! / d) * m;
      v.y += (dy.get(v)! / d) * m;
    }
    temp *= 0.985;
  }
  chooseOrientation(nodes, edges);
}

function separate(nodes: LNode[]): void {
  const n = nodes.length;
  for (let pass = 0; pass < 60; pass++) {
    let moved = false;
    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        const u = nodes[i];
        const v = nodes[j];
        const ox = (u.w + v.w) / 2 + 28 - Math.abs(u.x - v.x);
        const oy = (u.h + v.h) / 2 + 28 - Math.abs(u.y - v.y);
        if (ox > 0 && oy > 0) {
          moved = true;
          if (ox < oy) {
            const s = (u.x < v.x ? -1 : 1) * (ox / 2);
            u.x += s;
            v.x -= s;
          } else {
            const s = (u.y < v.y ? -1 : 1) * (oy / 2);
            u.y += s;
            v.y -= s;
          }
        }
      }
    }
    if (!moved) break;
  }
}

function center(nodes: LNode[]): void {
  const n = nodes.length;
  let cx = 0;
  let cy = 0;
  for (const v of nodes) {
    cx += v.x;
    cy += v.y;
  }
  cx /= n;
  cy /= n;
  for (const v of nodes) {
    v.x -= cx;
    v.y -= cy;
  }
}

/**
 * The force layout has no preferred direction: try the principal-axis
 * orientation (and its mirror images) and keep the one that fills a wide
 * canvas best while keeping subtypes below their parents.
 */
function chooseOrientation(nodes: LNode[], edges: LEdge[]): void {
  center(nodes);
  let sxx = 0;
  let syy = 0;
  let sxy = 0;
  for (const v of nodes) {
    sxx += v.x * v.x;
    syy += v.y * v.y;
    sxy += v.x * v.y;
  }
  const theta = 0.5 * Math.atan2(2 * sxy, sxx - syy); // angle of the principal axis
  const base = nodes.map((v) => ({ x: v.x, y: v.y }));
  const byId = new Map(nodes.map((v, i) => [v.id, i]));
  const ext = edges.filter((e) => e.kind === 'extends' && byId.has(e.a) && byId.has(e.b));
  let best: { x: number; y: number }[] | undefined;
  let bestScore = -1;
  for (const [rot, flip] of [
    [0, false],
    [-theta, false],
    [-theta, true],
    [-theta + Math.PI, false],
    [-theta + Math.PI, true],
  ] as [number, boolean][]) {
    const c = Math.cos(rot);
    const s = Math.sin(rot);
    nodes.forEach((v, i) => {
      const x = base[i].x * c - base[i].y * s;
      const y = base[i].x * s + base[i].y * c;
      v.x = flip ? -x : x;
      v.y = y;
    });
    separate(nodes);
    let x0 = Infinity;
    let x1 = -Infinity;
    let y0 = Infinity;
    let y1 = -Infinity;
    for (const v of nodes) {
      x0 = Math.min(x0, v.x - v.w / 2);
      x1 = Math.max(x1, v.x + v.w / 2);
      y0 = Math.min(y0, v.y - v.h / 2);
      y1 = Math.max(y1, v.y + v.h / 2);
    }
    const fitScale = Math.min(640 / (x1 - x0), 450 / (y1 - y0));
    let wrong = 0;
    for (const e of ext) if (nodes[byId.get(e.a)!].y <= nodes[byId.get(e.b)!].y) wrong++;
    const score = fitScale * Math.pow(0.9, wrong);
    if (score > bestScore) {
      bestScore = score;
      best = nodes.map((v) => ({ x: v.x, y: v.y }));
    }
  }
  nodes.forEach((v, i) => {
    v.x = best![i].x;
    v.y = best![i].y;
  });
  center(nodes);
}
