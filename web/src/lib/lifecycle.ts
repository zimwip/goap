// Node lifecycles as seen by a change (ADR 0014): the state a node has in the
// change once its change impacts are counted, and the transitions it can take.
import { graph, type ChangeImpact, type GraphNode, type Lifecycle, type LifecycleTransition, type LinkWrite, type NodeRef } from './api';
import type { TypeCatalog } from './stores/types.svelte';

/** Lifecycle of a node type, from the type catalogue (extends chain included). */
export function lifecycleResolver(cat: TypeCatalog): (type: string | undefined) => Lifecycle | undefined {
  return (type) => cat.lifecycle(type);
}

export interface LifecycleRow {
  node: GraphNode;
  /** undefined: the node type has no lifecycle (always landable) */
  lifecycle?: Lifecycle;
  /** stored state ('' : the node has none yet) */
  base: string;
  /** state after the change's transitions */
  effective: string;
  /** states the change moves the node through */
  moves: string[];
  /** the effective state can land: a node of a type without lifecycle, or in a state not flagged notLandable (ADR 0078) */
  landable: boolean;
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
  /** the change impact of the node: the one standing in the flow shown, else the last one declared on it (rejected) */
  impact?: ChangeImpact;
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

/** Is the post version of a change impact a working version (checked out, ADR 0076)? */
async function workingPost(cn: ChangeImpact): Promise<GraphNode | undefined> {
  if (!cn.post?.id) return undefined;
  const n = (await graph.getNode({ id: cn.post.id, version: cn.post.version })).view?.node;
  return n?.checkedOut ? n : undefined;
}

/** Accepts a change impact (the UI is its own reviewer) and checks its working version in; a version the check-in
 * refuses (a validator, a required link) is sent back to proposed, to be completed. */
async function acceptAndCheckin(changeId: string, cn: ChangeImpact, rationale: string, flow: string): Promise<ChangeImpact> {
  if (cn.review !== 'accepted') cn = (await graph.impactNodeReview(changeId, cn.id!, true, rationale, flow)).node ?? cn;
  if (!(await workingPost(cn))) return cn;
  try {
    return (await graph.impactNodeCheckin(changeId, cn.id!, flow)).node ?? cn;
  } catch (e) {
    if (!flow || flow === 'main') await graph.reopenChangeImpacts(changeId, [cn.id!], 'the check-in was refused');
    throw e;
  }
}

/** Writes a node of the change (ADR 0076): created or checked out (once per write: a working version), its
 * properties and links edited in place, accepted (the UI is its own reviewer) and checked in; a state is then a
 * transition of its own. A node is never deleted: a node the change created is taken out of it (removeFromChange),
 * one no parent holds is retired by its lifecycle. */
export async function writeNodeInChange(
  changeId: string,
  cns: ChangeImpact[],
  target: { pre?: NodeRef; key?: string; type?: string },
  w: { props?: Record<string, unknown>; state?: string; addLinks?: LinkWrite[]; removeLinks?: string[] },
  rationale: string,
  flow = '',
): Promise<void> {
  // flow: the flow or option written on ('main' names the main flow, '' is the active option); cns is its view
  const scoped = flow !== '';
  let cn = cns.find((c) => live(c, scoped) && (target.pre ? c.pre?.id === target.pre.id : c.key === target.key && c.intent === 'created'));
  const edits = !!(w.props && Object.keys(w.props).length) || !!w.addLinks?.length || !!w.removeLinks?.length;
  if (!cn && !target.pre) {
    cn = (await graph.impactNodeCreate(changeId, { key: target.key ?? '', type: target.type ?? '', props: w.props as never, rationale, links: w.addLinks }, flow)).node;
    if (!cn?.id) throw new Error('The node could not be created.');
    cn = await acceptAndCheckin(changeId, cn, rationale, flow);
  } else if (edits) {
    let work = cn ? await workingPost(cn) : undefined;
    if (cn && work && cn.review === 'accepted') {
      // an accepted version is checked in, then checked out again to be changed
      cn = (await graph.impactNodeCheckin(changeId, cn.id!, flow)).node ?? cn;
      work = undefined;
    }
    if (!work) {
      cn = (await graph.impactNodeCheckout(changeId, cn?.id ? { changeImpactId: cn.id } : { nodeId: target.pre?.id }, rationale, flow)).node;
      if (!cn?.id || !cn.post) throw new Error('The node could not be checked out.');
    }
    const id = cn!.id!;
    if (w.props && Object.keys(w.props).length) await graph.impactNodeUpdate(changeId, id, { props: w.props as never }, flow);
    for (const l of w.addLinks ?? []) await graph.impactLinkCreate(changeId, id, l, flow);
    if (w.removeLinks?.length) {
      // the ids name links of the version the caller read: the working version carries copies of them
      const out = (await graph.getNode({ id: cn!.post!.id, version: cn!.post!.version })).view?.out ?? [];
      const read = target.pre ? ((await graph.getNode(target.pre)).view?.out ?? []) : [];
      for (const lid of w.removeLinks) {
        const was = out.find((l) => l.id === lid) ?? read.find((l) => l.id === lid);
        const copy = out.find((l) => l.id === lid) ?? out.find((l) => was && l.type === was.type && l.to?.id === was.to?.id);
        if (copy?.id) await graph.impactLinkDelete(changeId, copy.id, flow);
      }
    }
    cn = await acceptAndCheckin(changeId, cn!, rationale, flow);
  } else if (cn && (await workingPost(cn))) {
    cn = await acceptAndCheckin(changeId, cn, rationale, flow);
  }
  if (w.state) {
    const moved = (await graph.impactNodeTransition(changeId, cn?.id ? { changeImpactId: cn.id } : { nodeId: target.pre?.id }, w.state, rationale, flow)).node;
    // the UI is its own reviewer: the version of the transition is accepted with it
    if (moved?.id && moved.review !== 'accepted') await graph.impactNodeReview(changeId, moved.id, true, rationale, flow);
  }
}

/** Takes a node the change works on out of it: its working version is dropped (a node the change created and never
 * checked in goes away); refused once a version of it is checked in (reject it instead). */
export async function removeFromChange(changeId: string, cn: ChangeImpact, flow = ''): Promise<void> {
  if (cn.id) await graph.withdrawImpact(changeId, cn.id, flow);
}

/** Checks in the accepted working versions of a change: their acceptance authorizes it, and a change is applied
 * with every version checked in (ADR 0076). */
export async function checkinAccepted(changeId: string): Promise<void> {
  const c = (await graph.getChange(changeId)).change;
  for (const cn of c?.nodes ?? []) {
    if (cn.review === 'accepted' && !cn.flow && !cn.superseded && (await workingPost(cn))) await graph.impactNodeCheckin(changeId, cn.id!, 'main');
  }
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
      landable: lifecycle ? landableState(lifecycle, state) : true,
      transitions: [],
      props: { ...((post?.props ?? {}) as Record<string, unknown>) },
      edits: 0,
      declared: cat.properties(cn.type),
      created: cn,
      impact: cn,
    });
  }
  return rows;
}

const landableState = (l: Lifecycle, s: string) => !s || !l.states?.find((x) => x.name === s)?.notLandable;

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
    const cn = cns.find((c) => c.intent === 'modified' && c.pre?.id === id && live(c, scoped));
    const post = cn ? postOf(cn, posts) : undefined;
    if (cn && post) {
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
      landable: lifecycle ? landableState(lifecycle, cur) : true,
      transitions: lifecycle ? (lifecycle.transitions ?? []).filter((t) => t.from === cur) : [],
      props,
      edits,
      declared: cat.properties(node.type),
      impact: cn ?? cns.filter((c) => c.pre?.id === id && !c.superseded).at(-1),
    });
  }
  rows.sort((a, b) => (a.node.key ?? '').localeCompare(b.node.key ?? ''));
  return [...rows, ...createdRows(cat, cns, posts, scoped)];
}

/** Nodes of the baseline the change could take on: of its namespace (every one without), not deleted. */
export function reopenable(nodes: GraphNode[], rows: LifecycleRow[], namespace = ''): GraphNode[] {
  const have = new Set(rows.map((r) => r.node.id));
  return nodes.filter((n) => !n.deleted && !have.has(n.id) && (!namespace || n.namespace === namespace));
}
