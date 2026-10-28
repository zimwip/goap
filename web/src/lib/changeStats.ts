// The counters of a change (what its commits did to the graph, net per node, and the impacts not committed yet), as the
// change tab shows them: pure, over the counters the graph answers with the change.
import type { ChangeStats } from './api/types/graph';

/** One counter shown: its label, its value and what it counts. */
export interface Counter {
  key: string;
  label: string;
  value: number;
  title: string;
}

/** The groups of counters: the nodes committed, the properties and links they changed, the impacts still pending. */
export interface CounterGroups {
  nodes: Counter[];
  content: Counter[];
  pending: Counter[];
  /** nothing committed and nothing pending */
  empty: boolean;
}

function counter(key: string, label: string, value: number | undefined, title: string): Counter | null {
  return value ? { key, label, value, title } : null;
}

const present = (cs: (Counter | null)[]): Counter[] => cs.filter((c): c is Counter => c !== null);

/** counterGroups gives the counters that are not zero, grouped. */
export function counterGroups(s: ChangeStats | undefined): CounterGroups {
  const v = s ?? {};
  const nodes = present([
    counter('created', 'created', v.created, 'nodes the change created'),
    counter('modified', 'modified', v.modified, 'nodes the change modified'),
    counter('deleted', 'deleted', v.deleted, 'nodes the change deleted'),
    counter('restored', 'restored', v.restored, 'deleted nodes the change restored'),
    counter('requalified', 'requalified', v.requalified, 'creations of a key the graph held, taken as the modification of that node'),
  ]);
  const content = present([
    counter('properties', 'properties', v.properties, 'properties set, changed or unset'),
    counter('states', 'state moves', v.states, 'lifecycle states moved'),
    counter('linksAdded', 'links added', v.linksAdded, 'outgoing links added'),
    counter('linksRemoved', 'links removed', v.linksRemoved, 'outgoing links removed'),
    counter('linksChanged', 'links changed', v.linksChanged, 'outgoing links whose properties changed'),
  ]);
  const pending = present([
    counter('proposed', 'proposed', v.proposed, 'impacts not committed, awaiting a review'),
    counter('accepted', 'accepted', v.accepted, 'impacts accepted, not committed yet'),
    counter('rejected', 'rejected', v.rejected, 'impacts rejected, not committed'),
  ]);
  return { nodes, content, pending, empty: !nodes.length && !content.length && !pending.length };
}
