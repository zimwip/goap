// Node lifecycles as seen by a change (ADR 0014): the state a node has in the
// change once its change impacts are counted, and the transitions it can take.
import { graph, isDraft, type ChangeImpact, type GraphNode, type Lifecycle, type LifecycleTransition, type LinkWrite, type NodeRef } from './api';
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
  /** the change holds a draft of the node (ADR 0079): it has no version of its own until the change lands */
  draft: boolean;
  /** properties declared by the node type (inherited ones included) */
  declared: string[];
  /** the type is free-form (`additionalProperties`): properties that are no attribute may be set */
  open: boolean;
  /** the change impact, for a node the change creates (not stored yet) */
  created?: ChangeImpact;
  /** the change impact of the node: the one standing in the flow shown, else the last one declared on it (rejected) */
  impact?: ChangeImpact;
}

/** What the change impacts hold of their nodes, by `postKey`: the DRAFT view of a node the change works on (ADR 0079:
 * no version while the change works on it, the impact's post is a draft reference), the version a landed impact wrote
 * otherwise. The change impacts carry references only. */
export type PostVersions = Map<string, GraphNode>;

/** Key of an impact's post in `PostVersions`: a draft has no version, so it is `<id>@draft`. */
export const postKey = (ref: { id?: string; version?: number }): string => `${ref.id}@${isDraft(ref) ? 'draft' : ref.version}`;

/** Loads what the impacts' posts name. A draft is read through the change (`getNode` with the change and flow, ADR
 * 0079), never by a version; a landed post is one of the versions of its node. `scope.flow`: the flow the impacts are the view of. */
export async function loadPosts(cns: ChangeImpact[], scoped = false, scope?: { changeId: string; flow?: string }): Promise<PostVersions> {
  const out: PostVersions = new Map();
  const posts = cns.filter((c) => c.post?.id && live(c, scoped)).map((c) => c.post!);
  const drafts = posts.filter((p) => isDraft(p));
  const ids = [...new Set(posts.filter((p) => !isDraft(p)).map((p) => p.id!))];
  await Promise.all([
    ...ids.map(async (id) => {
      for (const v of (await graph.listNodeVersions(id)).versions ?? []) out.set(`${id}@${v.version}`, v);
    }),
    ...(scope
      ? [...new Map(drafts.map((d) => [d.id, d])).values()].map(async (d) => {
          const n = (await graph.getNode(d, undefined, scope)).view?.node;
          if (n) out.set(postKey(d), n);
        })
      : []),
  ]);
  return out;
}

/** Change impacts standing in the flow shown: not replaced, not rejected. A scoped list (the view of one flow or
 * option, ADR 0032 §6) holds only what that flow sees; the whole list of the change keeps the main flow's. */
const live = (c: ChangeImpact, scoped = false) => !c.superseded && (scoped || !c.flow) && c.review !== 'rejected';

const postOf = (c: ChangeImpact, posts: PostVersions): GraphNode | undefined => (c.post?.id ? posts.get(postKey(c.post)) : undefined);

/** Whether the impact holds a draft of its node in the flow written on. The post of an impact as a flow sees it is a
 * draft reference when the flow's chain holds a draft, but a draft held only by a PARENT flow is not the flow's own
 * (ADR 0079): an impact declared on the main flow, seen from an option, needs the option's own checkout. `flow`: ''
 * is the active option (unknown here: the impact's draft is taken as its own), 'main' the main flow. */
export function holdsOwnDraft(cn: ChangeImpact | undefined, flow: string): boolean {
  if (!cn || !isDraft(cn.post)) return false;
  if (flow === '') return true;
  return flow === 'main' ? !cn.flow : cn.flow === flow;
}

const alreadyCheckedOut = (e: unknown) => /already checked out/i.test(e instanceof Error ? e.message : String(e));

/** Impacts of the flow shown that wait for their review (the graph refuses to land the change until each one is
 * reviewed: 'change impact X awaits its review'). */
export const awaitingReview = (cns: ChangeImpact[], scoped = false): ChangeImpact[] =>
  cns.filter((c) => live(c, scoped) && !!c.id && (c.review === 'proposed' || !c.review) && !!c.post?.id);

/** Accepts every impact awaiting its review, one explicit review each with the comment given: an explicit user action
 * (the "Accept all proposed" button), never a side effect of an edit (ADR 0079 / requirement: no automatic review). */
export async function acceptAllProposed(changeId: string, cns: ChangeImpact[], comment: string, flow = '', scoped = false): Promise<void> {
  for (const c of awaitingReview(cns, scoped)) await graph.impactNodeReview(changeId, c.id!, true, comment, flow);
}

/** Writes a node of the change (ADR 0076, 0079): created, or checked out when the flow holds no draft of it, its
 * properties and links edited in place on the DRAFT; a state is a transition (on the draft: a node with none is checked
 * out first). Nothing here reviews: an edit leaves the impact `proposed` (an edit sends an accepted one back to
 * proposed) and the review is a separate explicit action (ChangeLifecycle, acceptAllProposed). The version is written
 * when the change lands. A node is never deleted: a node the change created is taken out of it (removeFromChange), one
 * no parent holds is retired by its lifecycle. */
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
  } else if (edits) {
    // the flow's own draft is updated, never checked out again; a draft of a parent flow is not its own: checked out
    // (a conflict 'already checked out' means the flow had one after all: update it)
    if (!holdsOwnDraft(cn, flow)) {
      try {
        cn = (await graph.impactNodeCheckout(changeId, cn?.id ? { changeImpactId: cn.id } : { nodeId: target.pre?.id }, rationale, flow)).node ?? cn;
      } catch (e) {
        if (!alreadyCheckedOut(e) || !cn?.id) throw e;
      }
      if (!cn?.id || !cn.post) throw new Error('The node could not be checked out.');
    }
    const id = cn!.id!;
    const scope = { changeId, flow };
    if (w.props && Object.keys(w.props).length) await graph.impactNodeUpdate(changeId, id, { props: w.props as never }, flow);
    for (const l of w.addLinks ?? []) await graph.impactLinkCreate(changeId, id, l, flow);
    if (w.removeLinks?.length) {
      // the ids name links of the version the caller read, or of the draft view; the draft carries copies of the former
      const out = (await graph.getNode(cn!.post!, undefined, scope)).view?.out ?? [];
      const read = target.pre ? ((await graph.getNode(target.pre)).view?.out ?? []) : [];
      for (const lid of w.removeLinks) {
        const was = out.find((l) => l.id === lid) ?? read.find((l) => l.id === lid);
        const copy = out.find((l) => l.id === lid) ?? out.find((l) => was && l.type === was.type && l.to?.id === was.to?.id);
        if (copy?.id) await graph.impactLinkDelete(changeId, copy.id, flow);
      }
    }
  }
  if (w.state) {
    // a transition on a node with no draft checks it out first; it never changes the review
    await graph.impactNodeTransition(changeId, cn?.id ? { changeImpactId: cn.id } : { nodeId: target.pre?.id }, w.state, rationale, flow);
  }
}

/** Takes a node the change works on out of it: its draft is dropped (a node the change created goes away);
 * refused once its version has landed (reject it instead). */
export async function removeFromChange(changeId: string, cn: ChangeImpact, flow = ''): Promise<void> {
  if (cn.id) await graph.withdrawImpact(changeId, cn.id, flow);
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
      draft: true,
      declared: cat.properties(cn.type),
      open: cat.open(cn.type),
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
    const cn = cns.find((c) => c.intent === 'modified' && c.pre?.id === id && live(c, scoped));
    const post = cn ? postOf(cn, posts) : undefined;
    if (cn && post) {
      if (post.state && post.state !== cur) {
        cur = post.state;
        moves.push(cur);
      }
      Object.assign(props, (post.props ?? {}) as Record<string, unknown>);
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
      draft: !!cn && isDraft(cn.post),
      declared: cat.properties(node.type),
      open: cat.open(node.type),
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
