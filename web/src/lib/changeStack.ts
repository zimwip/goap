// The change-stack of a node version or a commit (ADR 0133), as the web reads it: one row per change, oldest first,
// a sub-change under its parent, with what it did in the lineage (the versions it wrote, rebased or made join a branch,
// the commits it left). Pure: the component loads the stack and renders the rows.
import { shortId, type ChangeStack, type StackChange } from './api';

export interface StackRow {
  id: string;
  /** the title, else a short id (a change the caller may not read, or purged) */
  label: string;
  status?: string;
  /** the change it is a sub-change of, when that change is in the stack too */
  parent?: string;
  /** how many of its ancestors the stack holds: its indentation */
  depth: number;
  /** what it did in the lineage, in its order */
  how: string[];
}

export const changeLabel = (c: StackChange | undefined, id = c?.id ?? '') => c?.title || `change ${shortId(id)}`;

/** The rows of a stack. */
export function stackRows(stack: ChangeStack | undefined): StackRow[] {
  const changes = stack?.changes ?? [];
  const byId = new Map(changes.map((c) => [c.id ?? '', c]));
  const how = new Map<string, string[]>();
  const add = (id: string | undefined, what: string) => {
    if (!id) return;
    const l = how.get(id) ?? [];
    if (!l.includes(what)) l.push(what);
    how.set(id, l);
  };
  for (const v of stack?.versions ?? []) {
    const parents = v.parents ?? [];
    const on = v.branch || 'main';
    if (v.reason === 'rebase' || v.reason === 'merge') add(v.change, `${v.reason}d v${v.version} (v${parents.join(' + v')}) on ${on}`);
    else add(v.change, `wrote v${v.version}${v.reason ? ` (${v.reason})` : ''} on ${on}`);
    for (const j of v.joined ?? []) add(j.change, `made v${v.version} join ${j.branch || 'main'}`);
  }
  for (const b of stack?.commits ?? []) {
    add(b.change, `${b.kind || 'commit'} on ${b.branch || 'main'}`);
    // a sub-change leaves no commit of its own (ADR 0140): it came in merged into the change of the commit
    for (const m of b.merged ?? []) add(m, `merged into ${changeLabel(byId.get(b.change ?? ''), b.change ?? '')}`);
  }
  const depthOf = (id: string, seen = new Set<string>()): number => {
    const p = byId.get(id)?.parent;
    if (!p || !byId.has(p) || seen.has(p)) return 0;
    seen.add(p);
    return 1 + depthOf(p, seen);
  };
  return changes.map((c) => {
    const id = c.id ?? '';
    const parent = c.parent && byId.has(c.parent) ? c.parent : undefined;
    return { id, label: changeLabel(c), status: c.status || undefined, parent, depth: depthOf(id), how: how.get(id) ?? [] };
  });
}

/** One line naming the changes of a stack: "A + B (sub-change of P) + P". */
export function stackLine(stack: ChangeStack | undefined): string {
  const rows = stackRows(stack);
  const byId = new Map(rows.map((r) => [r.id, r]));
  return rows.map((r) => (r.parent ? `${r.label} (sub-change of ${byId.get(r.parent)?.label})` : r.label)).join(' + ');
}
