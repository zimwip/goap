// Pending edits of the settings dialog. Everything the dialog edits is graph data of the platform or the
// organisation namespace, and every modification of the graph goes through a change: an edit opens, behind the
// scenes, a personal change of the namespace of the node (ADR 0037: held by the personal unit of the user, nobody
// else sees it) and is written as an impact of it. Nothing is applied until the user saves: Save accepts the
// impacts and applies the changes, Discard removes them from the system. The dialog shows the graph with the
// pending nodes laid over it (see llmEdit.ts for the model gateway).
import { graph, errorMessage, type NodeRef, type Struct } from '../api';
import { MAIN_BRANCH } from '../namespace';
import { me, userKey } from './session.svelte';

/** One node touched by the user, not applied yet. */
export interface Staged {
  key: string;
  type: string;
  /** the properties the user wants (upsert) */
  props: Struct;
  /** the user removes the node */
  retire: boolean;
  /** the node does not exist on main yet */
  created: boolean;
  impactId: string;
  /** a version was written for it (an impact declared but never written is given up when saving) */
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

let chain: Promise<unknown> = Promise.resolve(); // edits are written one after the other
const sequential = <T>(fn: () => Promise<T>): Promise<T> => {
  const run = chain.then(fn, fn);
  chain = run.catch(() => undefined);
  return run;
};

async function mainHead(ns: string): Promise<string> {
  const b = await graph.getBranch(ns, MAIN_BRANCH);
  const id = b.head?.id ?? b.branch?.head;
  if (!id) throw new Error(`the ${ns} namespace has no baseline yet`);
  return id;
}

/** The node of main with this key, when it exists. */
async function onMain(ns: string, baselineId: string, type: string, key: string): Promise<NodeRef | undefined> {
  const { nodes } = await graph.listBaselineNodes({ baselineId, type, query: key, limit: 50 });
  const n = nodes?.find((x) => x.key === key && x.namespace === ns && !x.deleted);
  return n ? { id: n.id, version: n.version } : undefined;
}

async function ensure(ns: string, type: string, key: string): Promise<{ c: NsChange; s: Staged }> {
  let c = pending.byNs[ns];
  const baselineId = await mainHead(ns);
  if (!c) {
    const { change } = await graph.createChange({
      title: `My ${ns} settings`,
      intent: `Change the ${ns} settings`,
      namespace: ns,
      baselineId,
      ownBranch: true,
      ownerOrg: '@me',
    });
    if (!change?.id) throw new Error('change not created');
    pending.byNs[ns] = { changeId: change.id, nodes: {} };
    c = pending.byNs[ns];
  }
  let s = c.nodes[key];
  if (!s) {
    const pre = await onMain(ns, baselineId, type, key);
    const impact = pre
      ? { intent: 'modified', pre, rationale: `Change ${key}` }
      : { intent: 'created', key, type, rationale: `Create ${key}` };
    const { nodes } = await graph.addChangeImpacts(c.changeId, [impact]);
    const id = nodes?.[0]?.id;
    if (!id) throw new Error(`impact of ${key} not created`);
    c.nodes[key] = { key, type, props: {}, retire: false, created: !pre, impactId: id, written: false };
    s = c.nodes[key];
  }
  return { c, s };
}

/** Stages the creation or the new properties of a node. */
export function stageUpsert(ns: string, type: string, key: string, props: Struct): Promise<void> {
  return sequential(async () => {
    try {
      const { c, s } = await ensure(ns, type, key);
      await graph.writeChangeImpact(c.changeId, s.impactId, { props });
      s.props = { ...s.props, ...props };
      s.retire = false;
      s.written = true;
      pending.error = '';
    } catch (e) {
      pending.error = errorMessage(e);
      throw e;
    }
  });
}

/** Stages the removal of a node: a node created by the pending change is simply given up. */
export function stageRetire(ns: string, type: string, key: string): Promise<void> {
  return sequential(async () => {
    try {
      const staged = pending.byNs[ns]?.nodes[key];
      if (staged?.created) {
        const changeId = pending.byNs[ns].changeId;
        await graph.reviewChangeImpact(changeId, staged.impactId, false, 'Given up before it was saved');
        delete pending.byNs[ns].nodes[key];
        // nothing left to save in this namespace: its change has no reason to stay
        if (!Object.keys(pending.byNs[ns].nodes).length) {
          await graph.deleteChange(changeId);
          delete pending.byNs[ns];
        }
      } else {
        const { c, s } = await ensure(ns, type, key);
        await graph.writeChangeImpact(c.changeId, s.impactId, { retire: true });
        s.retire = true;
        s.written = true;
      }
      pending.error = '';
    } catch (e) {
      pending.error = errorMessage(e);
      throw e;
    }
  });
}

/** Reads the pending changes of the user (an earlier session may have left some): they are offered again. */
export async function loadPending(): Promise<void> {
  const subject = me();
  if (!subject) {
    pending.loaded = true;
    return;
  }
  try {
    const byNs: Record<string, NsChange> = {};
    const mine = (await graph.listChanges()).changes?.filter((c) => c.ownerOrg === userKey(subject) && (c.status === 'draft' || c.status === 'active')) ?? [];
    for (const ch of mine) {
      if (!ch.id || !ch.namespace) continue;
      const entry: NsChange = { changeId: ch.id, nodes: {} };
      const board = (await graph.getBlackboard(ch.id, '')).change;
      for (const imp of board?.nodes ?? []) {
        if (imp.superseded || imp.review === 'rejected' || !imp.id) continue;
        const ref = imp.post ?? imp.pre;
        const n = ref ? (await graph.getNode(ref)).view?.node : undefined;
        const key = imp.key || n?.key;
        const type = imp.type || n?.type;
        if (!key || !type) continue;
        entry.nodes[key] = { key, type, props: (imp.post ? n?.props : undefined) ?? {}, retire: !!(imp.post && n?.deleted), created: imp.intent === 'created', impactId: imp.id, written: !!imp.post };
      }
      byNs[ch.namespace] = entry;
    }
    pending.byNs = byNs;
    pending.error = '';
  } catch (e) {
    pending.error = errorMessage(e);
  } finally {
    pending.loaded = true;
  }
}

/** Accepts the pending impacts and applies the changes: the edits become graph data. */
export function savePending(): Promise<boolean> {
  return sequential(async () => {
    pending.busy = true;
    try {
      for (const [ns, c] of Object.entries(pending.byNs)) {
        for (const s of Object.values(c.nodes)) {
          if (s.written) await graph.reviewChangeImpact(c.changeId, s.impactId, true, 'Saved by its owner');
          else await graph.reviewChangeImpact(c.changeId, s.impactId, false, 'Nothing was written');
        }
        await graph.applyChange(c.changeId, '');
        delete pending.byNs[ns];
      }
      pending.error = '';
      return true;
    } catch (e) {
      pending.error = errorMessage(e);
      return false;
    } finally {
      pending.busy = false;
    }
  });
}

/** Drops the pending edits: their changes are removed from the system. */
export function discardPending(): Promise<boolean> {
  return sequential(async () => {
    pending.busy = true;
    try {
      for (const [ns, c] of Object.entries(pending.byNs)) {
        await graph.deleteChange(c.changeId);
        delete pending.byNs[ns];
      }
      pending.error = '';
      return true;
    } catch (e) {
      pending.error = errorMessage(e);
      return false;
    } finally {
      pending.busy = false;
    }
  });
}
