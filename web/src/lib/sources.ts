// Sources of the logical URLs (ADR 0154): a URL lnk:<source>/<type>/<key> names its source; the platform declares the
// sources (platform@Source nodes, SRC:<name>, edited from the settings dialog like the model gateway's entries) and
// each project maps the names its URLs give to the declared sources holding them there (organisation@ProjectUnit
// `sources`, inherited by its sub-projects, the nearest wins). A name no project maps and no source declares is the
// namespace of that name of this graph.
import type { GraphNode, Link, Struct } from "./api";
import { stagedOfType } from "./stores/pending.svelte";

export const SOURCE_TYPE = "platform@Source";
export const SOURCE_PREFIX = "SRC:";
/** The resolvers the platform plugs (graphsvc.Resolvers); a service may add others, so the kind stays free text. */
export const SOURCE_KINDS = ["graph", "files"];
/** The name of a source (lnk.Source.Validate). */
export const SOURCE_NAME_RE = /^[a-z][a-z0-9_.-]*$/;

export const sourceKey = (name: string) => `${SOURCE_PREFIX}${name}`;

export interface Source {
  name: string;
  description: string;
  kind: string;
  namespace: string;
  location: string;
  branch: string;
  /** what else its resolver needs, as JSON text ('' for none) */
  params: string;
  /** an edit not saved yet */
  pending?: boolean;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** The source a node declares (domain.SourceOf), undefined for a key that is no source's. */
export function sourceOf(
  key: string,
  props: Record<string, unknown> | undefined,
): Source | undefined {
  if (!key.startsWith(SOURCE_PREFIX)) return undefined;
  const p = props ?? {};
  const params =
    p.params && typeof p.params === "object" && Object.keys(p.params).length
      ? JSON.stringify(p.params)
      : "";
  return {
    name: key.slice(SOURCE_PREFIX.length),
    description: str(p.description),
    kind: str(p.kind),
    namespace: str(p.namespace),
    location: str(p.location),
    branch: str(p.branch),
    params,
  };
}

/** What is wrong with a source before it is staged ('' when nothing): the checks of lnk.Source.Validate. */
export function sourceProblem(s: Source): string {
  if (!SOURCE_NAME_RE.test(s.name))
    return 'The name starts with a lowercase letter and holds lowercase letters, digits, ".", "_" or "-".';
  if (!s.kind.trim()) return "A source names the kind of resolver reading it.";
  if (s.kind.trim() === "files" && !s.location.trim())
    return "A source of files names its root directory.";
  if (s.params.trim()) {
    try {
      const v: unknown = JSON.parse(s.params);
      if (!v || typeof v !== "object" || Array.isArray(v))
        return "The parameters are a JSON object.";
    } catch {
      return "The parameters are not valid JSON.";
    }
  }
  return "";
}

/** The properties of a Source node: every key is sent, so that an emptied field clears the stored one (a save merges). */
export function sourceProps(s: Source): Struct {
  return {
    name: s.name,
    description: s.description.trim(),
    kind: s.kind.trim(),
    namespace: s.namespace.trim(),
    location: s.location.trim(),
    branch: s.branch.trim(),
    params: s.params.trim() ? (JSON.parse(s.params) as Struct) : {},
  };
}

/** The declared sources the settings show: the ones in force with the staged edits laid over them, by name. */
export function overlaySources(applied: GraphNode[], ns: string): Source[] {
  const rows = new Map<string, Source>();
  for (const n of applied) {
    const s =
      n.type === SOURCE_TYPE ? sourceOf(n.key ?? "", n.props) : undefined;
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
  return [...rows.values()].sort((a, b) => a.name.localeCompare(b.name));
}

// --- the mapping of a project --------------------------------------------------------------------------------

/** The mapping a project node holds itself (access.ProjectFromProps), name of the URL to declared source. */
export function ownSources(
  props: Record<string, unknown> | undefined,
): Record<string, string> {
  const v = props?.sources;
  const out: Record<string, string> = {};
  if (v && typeof v === "object" && !Array.isArray(v))
    for (const [k, t] of Object.entries(v))
      if (typeof t === "string" && t) out[k] = t;
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
export function sourcesInForce(
  key: string,
  propsOf: (key: string) => Record<string, unknown> | undefined,
  parentOf: (key: string) => string,
): SourceEntry[] {
  const out = new Map<string, SourceEntry>();
  const seen = new Set<string>();
  for (let k = key; k && !seen.has(k); k = parentOf(k)) {
    seen.add(k);
    for (const [name, source] of Object.entries(ownSources(propsOf(k))))
      if (!out.has(name)) out.set(name, { name, source, from: k });
  }
  return [...out.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/** The parent of each project by key, from the project_part_of links of a graph (a root links itself). */
export function projectParents(
  nodes: GraphNode[],
  links: Link[],
  linkType: string,
): Map<string, string> {
  const keyOf = new Map(nodes.map((n) => [n.id ?? "", n.key ?? ""]));
  const out = new Map<string, string>();
  for (const l of links) {
    if (l.type !== linkType || l.from?.id === l.to?.id) continue;
    const from = keyOf.get(l.from?.id ?? "");
    const to = keyOf.get(l.to?.id ?? "");
    if (from && to) out.set(from, to);
  }
  return out;
}
