// Sources of the logical URLs (ADR 0154): a URL lnk:<source>/<type>/<key> names its source; the platform declares the
// sources (platform@Source nodes, SRC:<name>, edited from the settings dialog like the model gateway's entries) and
// each project maps the names its URLs give to the declared sources holding them there (organisation@ProjectUnit
// `sources`, inherited by its sub-projects, the nearest wins). The kind of a source is its driver: a source of kind
// graph with a location and the namespaces it holds is another graph (the content graph, §9), every call and URL naming
// one of them going to it; the platform source is this graph. A name no source declares is the namespace of that name.
import type { GraphNode, Link, Struct } from './api';
import { stagedOfType } from './stores/pending.svelte';

export const SOURCE_TYPE = 'platform@Source';
export const SOURCE_PREFIX = 'SRC:';
/** The source of the platform graph (graphsvc.PlatformSource): the namespaces no other graph holds. */
export const PLATFORM_SOURCE = 'platform';
/** The drivers the platform plugs (graphsvc.Resolvers); a service may add others, so the kind stays free text. */
export const SOURCE_KINDS = ['graph', 'files'];
/** The name of a source (lnk.Source.Validate). */
export const SOURCE_NAME_RE = /^[a-z][a-z0-9_.-]*$/;

export const sourceKey = (name: string) => `${SOURCE_PREFIX}${name}`;

export interface Source {
  name: string;
  description: string;
  kind: string;
  namespace: string;
  location: string;
  /** the namespaces a graph at the location holds, comma separated ('' for none) */
  namespaces: string;
  branch: string;
  /** what else its resolver needs, as JSON text ('' for none) */
  params: string;
  /** an edit not saved yet */
  pending?: boolean;
}

const str = (v: unknown): string => (typeof v === 'string' ? v : '');

/** The names of a comma separated list, trimmed, the empty ones left out. */
export const namesOf = (list: string): string[] =>
  list
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

/** The source a node declares (domain.SourceOf), undefined for a key that is no source's. */
export function sourceOf(key: string, props: Record<string, unknown> | undefined): Source | undefined {
  if (!key.startsWith(SOURCE_PREFIX)) return undefined;
  const p = props ?? {};
  const params = p.params && typeof p.params === 'object' && Object.keys(p.params).length ? JSON.stringify(p.params) : '';
  const namespaces = Array.isArray(p.namespaces) ? p.namespaces.filter((n): n is string => typeof n === 'string').join(', ') : '';
  return { name: key.slice(SOURCE_PREFIX.length), description: str(p.description), kind: str(p.kind), namespace: str(p.namespace), location: str(p.location), namespaces, branch: str(p.branch), params };
}

/** What is wrong with a source before it is staged ('' when nothing): the checks of lnk.Source.Validate. */
export function sourceProblem(s: Source): string {
  if (!SOURCE_NAME_RE.test(s.name)) return 'The name starts with a lowercase letter and holds lowercase letters, digits, ".", "_" or "-".';
  if (!s.kind.trim()) return 'A source names the kind of driver reading it.';
  if (s.kind.trim() === 'files' && !s.location.trim()) return 'A source of files names its root directory.';
  if (namesOf(s.namespaces).length && (s.kind.trim() !== 'graph' || !s.location.trim())) return 'Only a graph at a location holds namespaces.';
  if (namesOf(s.namespaces).some((n) => !SOURCE_NAME_RE.test(n))) return 'A namespace is a lowercase name.';
  if (s.params.trim()) {
    try {
      const v: unknown = JSON.parse(s.params);
      if (!v || typeof v !== 'object' || Array.isArray(v)) return 'The parameters are a JSON object.';
    } catch {
      return 'The parameters are not valid JSON.';
    }
  }
  return '';
}

/** The properties of a Source node: every key is sent, so that an emptied field clears the stored one (a save merges). */
export function sourceProps(s: Source): Struct {
  return {
    name: s.name,
    description: s.description.trim(),
    kind: s.kind.trim(),
    namespace: s.namespace.trim(),
    location: s.location.trim(),
    namespaces: namesOf(s.namespaces),
    branch: s.branch.trim(),
    params: s.params.trim() ? (JSON.parse(s.params) as Struct) : {},
  };
}

/** The declared sources the settings show: the ones in force with the staged edits laid over them, the platform's first. */
export function overlaySources(applied: GraphNode[], ns: string): Source[] {
  const rows = new Map<string, Source>();
  for (const n of applied) {
    const s = n.type === SOURCE_TYPE ? sourceOf(n.key ?? '', n.props) : undefined;
    if (s) rows.set(s.name, s);
  }
  for (const st of stagedOfType(ns, SOURCE_TYPE)) {
    const name = st.key.slice(SOURCE_PREFIX.length);
    if (st.retire) {
      rows.delete(name);
      continue;
    }
    const s = sourceOf(st.key, st.props as Record<string, unknown>);
    if (s) rows.set(name, { ...s, pending: true });
  }
  return [...rows.values()].sort((a, b) => Number(b.name === PLATFORM_SOURCE) - Number(a.name === PLATFORM_SOURCE) || a.name.localeCompare(b.name));
}

/** What a source is, in a few words: this graph, another graph and what it holds, a directory... */
export function sourceRole(s: Source): string {
  if (s.kind === 'graph' && !s.location) return s.name === PLATFORM_SOURCE ? 'this graph (the platform)' : `this graph, namespace ${s.namespace || s.name}`;
  if (s.kind === 'graph') return namesOf(s.namespaces).length ? `graph holding ${namesOf(s.namespaces).join(', ')}` : `graph, namespace ${s.namespace || s.name}`;
  if (s.kind === 'files') return 'directory of files';
  return s.kind;
}

// --- the statistics of every graph -----------------------------------------------------------------------------

/** The namespaces of an overview grouped by the graph source holding them, the platform's first ('' is the platform). */
export function bySource<T extends { source?: string }>(items: T[]): { source: string; items: T[] }[] {
  const groups = new Map<string, T[]>();
  for (const n of items) {
    const s = n.source || PLATFORM_SOURCE;
    groups.set(s, [...(groups.get(s) ?? []), n]);
  }
  return [...groups.entries()]
    .map(([source, items]) => ({ source, items }))
    .sort((a, b) => Number(b.source === PLATFORM_SOURCE) - Number(a.source === PLATFORM_SOURCE) || a.source.localeCompare(b.source));
}

// --- the mapping of a project --------------------------------------------------------------------------------

/** The mapping a project node holds itself (access.ProjectFromProps), name of the URL to declared source. */
export function ownSources(props: Record<string, unknown> | undefined): Record<string, string> {
  const v = props?.sources;
  const out: Record<string, string> = {};
  if (v && typeof v === 'object' && !Array.isArray(v)) for (const [k, t] of Object.entries(v)) if (typeof t === 'string' && t) out[k] = t;
  return out;
}

/** One entry of the mapping in force on a project, and the project it comes from. */
export interface SourceEntry {
  name: string;
  source: string;
  from: string;
}

/**
 * The mapping in force on a project (access.Snapshot.ProjectSources): the mappings of its chain, the project first
 * then its parents up to the root, the nearest winning; `parentOf` gives the key of a project's parent ('' at the root).
 */
export function sourcesInForce(key: string, propsOf: (key: string) => Record<string, unknown> | undefined, parentOf: (key: string) => string): SourceEntry[] {
  const out = new Map<string, SourceEntry>();
  const seen = new Set<string>();
  for (let k = key; k && !seen.has(k); k = parentOf(k)) {
    seen.add(k);
    for (const [name, source] of Object.entries(ownSources(propsOf(k)))) if (!out.has(name)) out.set(name, { name, source, from: k });
  }
  return [...out.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/** The parent of each project by key, from the project_part_of links of a graph (a root links itself). */
export function projectParents(nodes: GraphNode[], links: Link[], linkType: string): Map<string, string> {
  const keyOf = new Map(nodes.map((n) => [n.id ?? '', n.key ?? '']));
  const out = new Map<string, string>();
  for (const l of links) {
    if (l.type !== linkType || l.from?.id === l.to?.id) continue;
    const from = keyOf.get(l.from?.id ?? '');
    const to = keyOf.get(l.to?.id ?? '');
    if (from && to) out.set(from, to);
  }
  return out;
}
