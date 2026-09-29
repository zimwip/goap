// Node lifecycles as seen by a change (ADR 0014): the state a node has in the
// change once its change impacts are counted, and the transitions it can take.
import { graph, type ChangeImpact, type GraphNode, type Lifecycle, type LifecycleTransition, type NodeRef } from './api';
import type { TypeCatalog } from './stores/types.svelte';

/** Lifecycle of a node type, from the type catalogue (extends chain included). */
export function lifecycleResolver(cat: TypeCatalog): (type: string | undefined) => Lifecycle | undefined {
  return (type) => cat.lifecycle(type);
}

export interface LifecycleRow {
  node: GraphNode;
  /** undefined: the node type has no lifecycle (always editable through a change) */
  lifecycle?: Lifecycle;
  /** stored state ('' : the node has none yet) */
  base: string;
  /** state after the change's transitions */
  effective: string;
  /** states the change moves the node through */
  moves: string[];
  editable: boolean;
  /** transitions available from the effective state */
  transitions: LifecycleTransition[];
  /** properties after the change's writes */
  props: Record<string, unknown>;
  /** number of versions the change wrote on this node */
  edits: number;
  /** properties declared by the node type (inherited ones included) */
  declared: string[];
  /** the change impact, for a node the change creates (not stored yet) */
  created?: ChangeImpact;
  /** the change impact whose written version retires this node */
  removal?: ChangeImpact;
}

/** Versions written by the change impacts, by `id@version` (the change impacts carry references only). */
export type PostVersions = Map<string, GraphNode>;

export async function loadPosts(cns: ChangeImpact[], scoped = false): Promise<PostVersions> {
  const out: PostVersions = new Map();
  const ids = [...new Set(cns.filter((c) => c.post?.id && live(c, scoped)).map((c) => c.post!.id!))];
  await Promise.all(
    ids.map(async (id) => {
      for (const v of (await graph.listNodeVersions(id)).versions ?? []) out.set(`${id}@${v.version}`, v);
    }),
  );
  return out;
}

/** Change impacts standing in the flow shown: not replaced, not rejected. A scoped list (the view of one flow or
 * option, ADR 0032 §6) holds only what that flow sees; the whole list of the change keeps the main flow's. */
const live = (c: ChangeImpact, scoped = false) => !c.superseded && (scoped || !c.flow) && c.review !== 'rejected';

const postOf = (c: ChangeImpact, posts: PostVersions): GraphNode | undefined => (c.post?.id ? posts.get(`${c.post.id}@${c.post.version}`) : undefined);

/** The versions written from `from` to `post`, following the parents: versions are numbered per node across every
 * branch and option (ADR 0032), so the difference of the numbers is not the number of edits. */
function editsBetween(posts: PostVersions, id: string, from: number, post: GraphNode): number {
  let n = 0;
  for (let v: GraphNode | undefined = post; v && (v.version ?? 0) > from && n < 1000; n++) {
    const parent: number | undefined = v.parents?.[0];
    v = parent ? posts.get(`${id}@${parent}`) : undefined;
  }
  return n;
}

/** Declares (once) and writes a node of the change, then accepts it: the UI is its own reviewer. */
export async function writeNodeInChange(
  changeId: string,
  cns: ChangeImpact[],
  target: { pre?: NodeRef; key?: string; type?: string },
  w: { props?: Record<string, unknown>; state?: string; retire?: boolean },
  rationale: string,
  flow = '',
): Promise<void> {
  // flow: the flow or option written on ('main' names the main flow, '' is the active option); cns is its view
  const scoped = flow !== '';
  let cn = cns.find((c) => live(c, scoped) && (target.pre ? c.pre?.id === target.pre.id : c.key === target.key && c.intent === 'created'));
  if (!cn) {
    const decl: ChangeImpact = target.pre ? { intent: 'modified', pre: target.pre, rationale } : { intent: 'created', key: target.key, type: target.type, rationale };
    if (flow) decl.flow = flow;
    cn = (await graph.addChangeImpacts(changeId, [decl])).nodes?.[0];
  }
  if (!cn?.id) throw new Error('The change impact could not be declared.');
  await graph.writeChangeImpact(changeId, cn.id, { props: w.props as never, state: w.state, retire: w.retire }, flow);
  await graph.reviewChangeImpact(changeId, cn.id, true, rationale, flow);
}

/** Qualified node types a change of the namespace can create (all of them without a namespace). */
export function nodeTypeNames(cat: TypeCatalog, namespace = ''): string[] {
  return cat.names(namespace);
}

/** States a new node of the type can be born in: the initial one, or one a transition leads to from it. */
export function birthStates(lifecycle: Lifecycle | undefined): string[] {
  if (!lifecycle) return [];
  const init = lifecycle.initial ?? '';
  return [init, ...(lifecycle.transitions ?? []).filter((t) => t.from === init).map((t) => t.to ?? '')].filter(
    (s, i, all) => s && all.indexOf(s) === i,
  );
}

/** Rows of the nodes the change creates. */
export function createdRows(cat: TypeCatalog, cns: ChangeImpact[], posts: PostVersions, scoped = false): LifecycleRow[] {
  const resolve = lifecycleResolver(cat);
  const rows: LifecycleRow[] = [];
  for (const cn of cns) {
    if (cn.intent !== 'created' || !cn.id || !live(cn, scoped)) continue;
    const post = postOf(cn, posts);
    const lifecycle = resolve(cn.type);
    const state = lifecycle ? post?.state || lifecycle.initial || '' : '';
    rows.push({
      node: { id: cn.id, key: cn.key ?? '', type: cn.type ?? '', version: 0, state },
      lifecycle,
      base: '',
      effective: state,
      moves: [],
      editable: lifecycle ? editableState(lifecycle, state) : true,
      transitions: [],
      props: { ...((post?.props ?? {}) as Record<string, unknown>) },
      edits: 0,
      declared: cat.properties(cn.type),
      created: cn,
    });
  }
  return rows;
}

const editableState = (l: Lifecycle, s: string) => !!l.states?.find((x) => x.name === s)?.editable;

/** Properties declared by a node type, its ancestors' first. */
export function declaredProperties(cat: TypeCatalog, type: string | undefined): string[] {
  return cat.properties(type);
}

/** Rows of the nodes a change works on (attached ones, plus `extra` ids picked by the user). */
export function lifecycleRows(
  cat: TypeCatalog,
  nodes: GraphNode[],
  attached: NodeRef[],
  cns: ChangeImpact[],
  posts: PostVersions,
  extra: string[],
  scoped = false,
): LifecycleRow[] {
  const resolve = lifecycleResolver(cat);
  const byId = new Map(nodes.map((n) => [n.id ?? '', n]));
  const ids = [...new Set([...attached.map((r) => r.id ?? ''), ...extra])].filter((id) => byId.has(id));
  const rows: LifecycleRow[] = [];
  for (const id of ids) {
    const node = byId.get(id)!;
    const lifecycle = resolve(node.type);
    const base = node.state ?? '';
    let cur = lifecycle ? base || lifecycle.initial || '' : '';
    const moves: string[] = [];
    const props: Record<string, unknown> = { ...((node.props ?? {}) as Record<string, unknown>) };
    let edits = 0;
    let removal: ChangeImpact | undefined;
    const cn = cns.find((c) => c.intent === 'modified' && c.pre?.id === id && live(c, scoped));
    const post = cn ? postOf(cn, posts) : undefined;
    if (cn && post) {
      if (post.deleted) removal = cn;
      if (post.state && post.state !== cur) {
        cur = post.state;
        moves.push(cur);
      }
      Object.assign(props, (post.props ?? {}) as Record<string, unknown>);
      edits = editsBetween(posts, id, node.version ?? 0, post);
    }
    rows.push({
      node,
      lifecycle,
      base,
      effective: cur,
      moves,
      editable: lifecycle ? editableState(lifecycle, cur) : true,
      transitions: lifecycle ? (lifecycle.transitions ?? []).filter((t) => t.from === cur) : [],
      props,
      edits,
      declared: cat.properties(node.type),
      removal,
    });
  }
  rows.sort((a, b) => (a.node.key ?? '').localeCompare(b.node.key ?? ''));
  return [...rows, ...createdRows(cat, cns, posts, scoped)];
}

/** Is this transition a "reopen": from a state that is not editable into one that is? */
export function isReopen(row: LifecycleRow, t: LifecycleTransition): boolean {
  return !!row.lifecycle && !row.editable && editableState(row.lifecycle, t.to ?? '');
}

/** Nodes of the baseline the change could take on: of its namespace (every one without), not deleted. */
export function reopenable(nodes: GraphNode[], rows: LifecycleRow[], namespace = ''): GraphNode[] {
  const have = new Set(rows.map((r) => r.node.id));
  return nodes.filter((n) => !n.deleted && !have.has(n.id) && (!namespace || n.namespace === namespace));
}
