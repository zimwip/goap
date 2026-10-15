// Pending edits of the settings dialog. Everything the dialog edits is graph data of the platform or the
// organisation namespace, and every modification of the graph goes through a change: an edit opens, behind the
// scenes, a personal change of the namespace of the node (ADR 0037: held by the personal unit of the user, nobody
// else sees it) and is written as an impact of it: the node is checked out and its draft edited in place (ADR 0079),
// a removal is the transition of its lifecycle that retires it (a node is never deleted, ADR 0076). Nothing is
// applied until the user saves: Save accepts the impacts and applies the changes, Discard removes them from the
// system. The server does those steps (ADR 0120: StageSettings, SaveSettings, DiscardSettings), one call per edit or
// batch of edits, and answers the whole pending state, which this store holds. The dialog shows the graph with the
// pending nodes laid over it (see llmEdit.ts for the model gateway).
import { graph, errorMessage, type PendingSettings, type SettingEdit, type Struct } from '../api';
import { me } from './session.svelte';

/** One node touched by the user, not applied yet. */
export interface Staged {
  key: string;
  type: string;
  /** the properties of its draft */
  props: Struct;
  /** the user retires the node (lifecycle state retired) */
  retire: boolean;
  /** the node does not exist on main yet */
  created: boolean;
  impactId: string;
  /** a draft was written for it (an impact declared but never written is given up when saving) */
  written: boolean;
}

interface NsChange {
  changeId: string;
  nodes: Record<string, Staged>;
}

export const pending = $state({
  byNs: {} as Record<string, NsChange>,
  busy: false,
  error: '',
  loaded: false,
});

/** Number of nodes waiting to be saved. */
export function pendingCount(): number {
  return Object.values(pending.byNs).reduce((n, c) => n + Object.values(c.nodes).filter((s) => s.written && !(s.created && s.retire)).length, 0);
}

export const isPending = (): boolean => Object.keys(pending.byNs).length > 0;

export function stagedNode(ns: string, key: string): Staged | undefined {
  return pending.byNs[ns]?.nodes[key];
}

/** The staged nodes of a namespace, of one type. */
export function stagedOfType(ns: string, type: string): Staged[] {
  return Object.values(pending.byNs[ns]?.nodes ?? {}).filter((s) => s.type === type);
}

/** The pending state the server answers, as the store holds it. */
export function fromServer(p: PendingSettings): Record<string, NsChange> {
  const byNs: Record<string, NsChange> = {};
  for (const c of p.changes ?? []) {
    if (!c.namespace || !c.changeId) continue;
    const nodes: Record<string, Staged> = {};
    for (const n of c.nodes ?? []) {
      nodes[n.key] = { key: n.key, type: n.type, props: n.props ?? {}, retire: !!n.retire, created: !!n.created, impactId: n.changeImpactId ?? '', written: !!n.written };
    }
    byNs[c.namespace] = { changeId: c.changeId, nodes };
  }
  return byNs;
}

let chain: Promise<unknown> = Promise.resolve(); // calls are made one after the other
const sequential = <T>(fn: () => Promise<T>): Promise<T> => {
  const run = chain.then(fn, fn);
  chain = run.catch(() => undefined);
  return run;
};

/** Stages edits in order, in one call. */
export function stageEdits(edits: SettingEdit[]): Promise<void> {
  if (!edits.length) return Promise.resolve();
  return sequential(async () => {
    try {
      pending.byNs = fromServer(await graph.stageSettings(edits));
      pending.error = '';
    } catch (e) {
      pending.error = errorMessage(e);
      // the edits before the refused one stay staged: show what the server holds
      await refresh();
      throw e;
    }
  });
}

/** Stages the creation or the new properties of a node. */
export const stageUpsert = (ns: string, type: string, key: string, props: Struct): Promise<void> => stageEdits([{ namespace: ns, type, key, props }]);

/** Stages the removal of a node: a node created by the pending change is simply given up. */
export const stageRetire = (ns: string, type: string, key: string): Promise<void> => stageEdits([{ namespace: ns, type, key, retire: true }]);

async function refresh(): Promise<void> {
  try {
    pending.byNs = fromServer(await graph.getPendingSettings());
  } catch {
    // keep what the store holds; the error shown is the first one
  }
}

/** Reads the pending changes of the user (an earlier session may have left some): they are offered again. */
export async function loadPending(): Promise<void> {
  if (!me()) {
    pending.loaded = true;
    return;
  }
  try {
    pending.byNs = fromServer(await graph.getPendingSettings());
    pending.error = '';
  } catch (e) {
    pending.error = errorMessage(e);
  } finally {
    pending.loaded = true;
  }
}

/** Saves or discards every pending edit, in one call. */
function settle(call: () => Promise<PendingSettings>): Promise<boolean> {
  return sequential(async () => {
    pending.busy = true;
    try {
      pending.byNs = fromServer(await call());
      pending.error = '';
      return true;
    } catch (e) {
      pending.error = errorMessage(e);
      await refresh();
      return false;
    } finally {
      pending.busy = false;
    }
  });
}

/** Accepts the pending impacts and applies the changes: the edits become graph data. */
export const savePending = (): Promise<boolean> => settle(graph.saveSettings);

/** Drops the pending edits: their changes are removed from the system. */
export const discardPending = (): Promise<boolean> => settle(graph.discardSettings);
