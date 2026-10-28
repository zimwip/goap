// The view the front works on (one for every screen): the working change of the header, on the option selected there,
// for the namespace it acts on; else, and for every other namespace, the head of its main branch. A screen reads of it
// only what it shows (a node's neighbourhood, a page of a search), never a namespace whole.
import { graph, type ChangeImpact, type GraphNode, type GraphView } from './api';
import { MAIN_SCOPE } from './changeScope';
import { commitHoods, branchView, changeHoods, type HoodLoader } from './graphIndex';
import { MAIN_BRANCH, namespaceOf } from './namespace';
import { working } from './stores/workingChange.svelte';

export interface ContextView {
  namespace: string;
  /** the commit searched: the fork of the change, else the head of main ('' : the namespace holds nothing yet) */
  commitId: string;
  /** the working change, when it acts on the namespace */
  changeId?: string;
  /** its option: '' the main flow */
  flow?: string;
  /** the impacts of the change */
  impacts?: ChangeImpact[];
}

/** The view a read of the graph names (ADR 0157): the change on its option, else the commit. */
export const graphView = (v: ContextView): GraphView => (v.changeId ? { changeId: v.changeId, flow: v.flow ?? '' } : branchView(v.namespace, v.commitId));

/** The view of a namespace now. */
export async function contextView(namespace: string, signal?: AbortSignal): Promise<ContextView> {
  const w = working.change;
  if (w && namespaceOf(w.namespace) === namespace) {
    const c = (await graph.getChange(w.id, signal)).change;
    const flow = working.option === MAIN_SCOPE ? '' : working.option;
    return { namespace, commitId: c?.forkCommit ?? '', changeId: w.id, flow, impacts: c?.nodes ?? [] };
  }
  const head = (await graph.getBranch(namespace, MAIN_BRANCH, signal)).head?.id ?? '';
  return { namespace, commitId: head };
}

/** The neighbourhoods of a view. */
export const viewHoods = (v: ContextView): HoodLoader => (v.changeId ? changeHoods(v.changeId, v.flow) : commitHoods(graphView(v)));

/** A key of the view: it changes when the view does. */
export const viewKey = (v: ContextView): string => (v.changeId ? `change:${v.changeId}:${v.flow ?? ''}` : `commit:${v.commitId}`);

/** The nodes of a view matching a text (a page of them), searched by the server in the view: a change's drafts and the
 * nodes it creates included. */
export async function searchView(v: ContextView, query: string, limit = 200, signal?: AbortSignal): Promise<GraphNode[]> {
  const found = (await graph.listCommitNodes({ view: graphView(v), query, limit }, signal)).nodes ?? [];
  return found.filter((n) => !n.deleted);
}
