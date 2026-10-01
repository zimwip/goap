// The roles a project needs (ADR 0043): a project names the methodologies that apply to it (inherited by its
// sub-projects), each declares its roles; an Assignment grants some of them to an organisation unit or a user
// on the project. Read from the head of the organisation namespace and the methodologies of the registry.
import { registry, type Methodology } from './api';
import type { HeadGraph } from './graphEdit';
import { PROJECT_UNIT_TYPE, PROJECT_PART_OF, ASSIGNMENT_TYPE, ASSIGNS_ORG, ASSIGNS_PROJECT } from './orgTypes';

const NS = 'organisation';

export interface ProjectRole {
  name: string;
  description: string;
  /** the applicable methodologies declaring it */
  methodologies: string[];
}

/** The project followed by its ancestors (project_part_of), nearest first. */
export function projectChain(head: HeadGraph, key: string): string[] {
  const byId = new Map(head.nodes.map((n) => [n.id ?? '', n]));
  const out: string[] = [];
  let cur = head.nodes.find((n) => n.namespace === NS && n.type === PROJECT_UNIT_TYPE && n.key === key);
  while (cur && !out.includes(cur.key ?? '')) {
    out.push(cur.key ?? '');
    const up = head.links.find((l) => l.type === PROJECT_PART_OF && l.from?.id === cur!.id && l.to?.id !== cur!.id);
    cur = up?.to?.id ? byId.get(up.to.id) : undefined;
  }
  return out;
}

/** The methodologies that apply to a project: its own and those of its ancestors. */
export function applicableMethodologies(head: HeadGraph, key: string): string[] {
  const out: string[] = [];
  for (const k of projectChain(head, key)) {
    const p = head.nodes.find((n) => n.namespace === NS && n.type === PROJECT_UNIT_TYPE && n.key === k);
    const list = p?.props?.['methodologies'];
    if (Array.isArray(list)) for (const m of list) if (typeof m === 'string' && !out.includes(m)) out.push(m);
  }
  return out;
}

const cache = new Map<string, Promise<Methodology | undefined>>();

/** The latest version of a methodology (cached for the session; undefined when the registry does not know it). */
function methodology(name: string): Promise<Methodology | undefined> {
  let p = cache.get(name);
  if (!p) {
    p = registry
      .getMethodology(name)
      .then((r) => r.methodology)
      .catch(() => undefined);
    cache.set(name, p);
  }
  return p;
}

/** Forgets the cached methodologies (after one was published). */
export function forgetMethodologies(): void {
  cache.clear();
}

/** The roles the given methodologies declare, by name, with the methodologies declaring each. */
export async function rolesOf(names: string[]): Promise<ProjectRole[]> {
  const out = new Map<string, ProjectRole>();
  for (const name of names) {
    const m = await methodology(name);
    for (const r of m?.roles ?? []) {
      if (!r.name) continue;
      const cur = out.get(r.name) ?? { name: r.name, description: r.description ?? '', methodologies: [] };
      if (!cur.description && r.description) cur.description = r.description;
      cur.methodologies.push(name);
      out.set(r.name, cur);
    }
  }
  return [...out.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/** The roles a project needs: those of its applicable methodologies. */
export const projectRoles = (head: HeadGraph, key: string): Promise<ProjectRole[]> => rolesOf(applicableMethodologies(head, key));

/** Who holds each role on a project (its Assignments, and those of its ancestors): role -> org unit / user keys. */
export function holders(head: HeadGraph, key: string): Map<string, string[]> {
  const chain = projectChain(head, key);
  const byId = new Map(head.nodes.map((n) => [n.id ?? '', n]));
  const target = (a: { id?: string }, type: string) => {
    const l = head.links.find((x) => x.type === type && x.from?.id === a.id);
    return l?.to?.id ? (byId.get(l.to.id)?.key ?? '') : '';
  };
  const out = new Map<string, string[]>();
  for (const a of head.nodes) {
    if (a.namespace !== NS || a.type !== ASSIGNMENT_TYPE || !chain.includes(target(a, ASSIGNS_PROJECT))) continue;
    const org = target(a, ASSIGNS_ORG);
    const roles = a.props?.['roles'];
    if (!org || !Array.isArray(roles)) continue;
    for (const r of roles) if (typeof r === 'string') out.set(r, [...(out.get(r) ?? []), org]);
  }
  return out;
}
