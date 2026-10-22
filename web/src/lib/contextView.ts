// The view the front works on (one for every screen): the working change of the header, on the option selected there,
// for the namespace it acts on; else, and for every other namespace, the head of its main branch. A screen reads of it
// only what it shows (a node's neighbourhood, a page of a search), never a namespace whole.
import { graph, type ChangeImpact, type GraphNode } from './api';
import { MAIN_SCOPE } from './changeScope';
import { baselineHoods, changeHoods, type HoodLoader } from './graphIndex';
import { MAIN_BRANCH, namespaceOf } from './namespace';
import { working } from './stores/workingChange.svelte';

export interface ContextView {
  namespace: string;
  /** the baseline searched: the fork of the change, else the head of main ('' : the namespace holds nothing yet) */
  baselineId: string;
  /** the working change, when it acts on the namespace */
  changeId?: string;
  /** its option: '' the main flow */
  flow?: string;
  /** the impacts of the change (the nodes it creates are not in its baseline) */
  impacts?: ChangeImpact[];
}

/** The view of a namespace now. */
export async function contextView(namespace: string, signal?: AbortSignal): Promise<ContextView> {
  const w = working.change;
  if (w && namespaceOf(w.namespace) === namespace) {
    const c = (await graph.getChange(w.id, signal)).change;
    const flow = working.option === MAIN_SCOPE ? '' : working.option;
    return { namespace, baselineId: c?.baselineId ?? '', changeId: w.id, flow, impacts: c?.nodes ?? [] };
  }
  const head = (await graph.getBranch(namespace, MAIN_BRANCH, signal)).head?.id ?? '';
  return { namespace, baselineId: head };
}

/** The neighbourhoods of a view. */
export const viewHoods = (v: ContextView): HoodLoader => (v.changeId ? changeHoods(v.changeId, v.flow) : baselineHoods(v.baselineId));

/** A key of the view: it changes when the view does. */
export const viewKey = (v: ContextView): string => (v.changeId ? `change:${v.changeId}:${v.flow ?? ''}` : `baseline:${v.baselineId}`);

/** The nodes of a view matching a text (a page of them): its baseline searched by the server, and the nodes the change
 * creates (the version 0 of their draft). */
export async function searchView(v: ContextView, query: string, limit = 200, signal?: AbortSignal): Promise<GraphNode[]> {
  const found = v.baselineId ? ((await graph.listBaselineNodes({ baselineId: v.baselineId, query, limit }, signal)).nodes ?? []) : [];
  const q = query.trim().toLowerCase();
  const created = (v.impacts ?? [])
    .filter((c) => c.intent === 'created' && (c.flow ?? '') === (v.flow ?? '') && c.post?.id && !c.superseded && c.review !== 'rejected' && (!q || `${c.key} ${c.type}`.toLowerCase().includes(q)))
    .map((c): GraphNode => ({ id: c.post?.id, version: 0, key: c.key, type: c.type, namespace: v.namespace }));
  return [...created, ...found.filter((n) => !n.deleted)].slice(0, limit);
}
