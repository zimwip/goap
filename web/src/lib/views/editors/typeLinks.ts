// Read-only views over the node type hierarchy and the link types around it (the domain editor).
import type { LinkTypeForm, NodeTypeForm } from '../../methodologyForm';

type TypeLike = Pick<NodeTypeForm, 'name' | 'extends'>;
type LinkLike = Pick<LinkTypeForm, 'name' | 'from' | 'to' | 'compose'>;

/** The type and its supertypes, nearest first (a subtype inherits the link types of its parents): name -> distance. */
export function typeChain(types: TypeLike[], name: string): Map<string, number> {
  const out = new Map<string, number>();
  const start = name.trim();
  if (!start) return out;
  out.set(start, 0);
  let cur = types.find((t) => t.name.trim() === start)?.extends ?? '';
  while (cur && !out.has(cur)) {
    out.set(cur, out.size);
    cur = types.find((t) => t.name.trim() === cur)?.extends ?? '';
  }
  return out;
}

/** The subtypes of a type, direct and indirect, each with its depth below the type (1: direct). */
export function subtypes(types: TypeLike[], name: string): { name: string; depth: number }[] {
  const out: { name: string; depth: number }[] = [];
  const seen = new Set<string>([name.trim()]);
  let level = [name.trim()];
  for (let depth = 1; level.length; depth++) {
    const next: string[] = [];
    for (const t of types) {
      const n = t.name.trim();
      if (n && !seen.has(n) && level.includes(t.extends)) {
        seen.add(n);
        next.push(n);
        out.push({ name: n, depth });
      }
    }
    level = next;
  }
  return out;
}

export interface LinkRow {
  /** index in the domain's link types */
  index: number;
  name: string;
  /** the type at the other end ('' : any) */
  other: string;
  compose: boolean;
  /** the supertype that declares the link, when the type only inherits it */
  inheritedFrom: string;
}

/** Link types leaving (`from`) or reaching (`to`) a node type or one of its supertypes. */
export function linksOf(types: TypeLike[], links: LinkLike[], typeName: string, end: 'from' | 'to'): LinkRow[] {
  const chain = typeChain(types, typeName);
  const other = end === 'from' ? 'to' : 'from';
  return links
    .map((l, i) => ({ l, i, depth: chain.get(l[end]?.trim() ?? '') }))
    .filter((r) => r.depth !== undefined && r.l.name.trim())
    .sort((a, b) => (a.depth ?? 0) - (b.depth ?? 0) || a.l.name.localeCompare(b.l.name))
    .map((r) => ({ index: r.i, name: r.l.name.trim(), other: r.l[other], compose: !!r.l.compose, inheritedFrom: r.depth ? r.l[end] : '' }));
}
