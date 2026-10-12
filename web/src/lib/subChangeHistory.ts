// The history of a change and its sub-changes as one branch graph: a lane per change, a row per event of the impact log of
// its main flow, and the edges that tie the lanes. A sub-change works on its own branch forked from its parent's (ADR
// 0103): its lane starts from where the parent stood, a rebase moves it onto the parent's branch, and its merge lands its
// commits in the parent (the parent's `landed` event of the same version). The logs written before ADR 0103 keep their
// edges: a copy of a parent's draft (the fork), a rebase onto the parent's draft, an integration of a sub-change's draft.
import type { LaneEntry, LaneEdge } from './components/HistoryGraph.svelte';
import { decodeLogEntry, shortId, type ImpactEvent, type LogEntry } from './api';

/** A change of the family: the parent, the change shown, its sub-changes. */
export interface FamilyMember {
  id: string;
  title?: string;
  parentId?: string;
}

/** One row: an event of the impact log of a change. */
export interface HistoryItem {
  change: string;
  changeTitle: string;
  seq: number;
  at: string;
  op: string;
  impact: string;
  key: string;
  label: string;
  by?: string;
  kind: 'edit' | 'review' | 'fork' | 'rebase' | 'integrate' | 'land';
}

type Event = ImpactEvent & {
  draft?: { inherited?: { changeId?: string; impactId?: string; seq?: number } };
  into?: { changeId?: string; impactId?: string };
};

const entryId = (change: string, seq: number) => `${change}:${seq}`;

function patchOf(e: Event): Record<string, unknown> {
  return (e.patch ?? {}) as Record<string, unknown>;
}

/** The change and log position that first landed a version, by `<node>@<version>`. */
type Landings = Map<string, { change: string; seq: number; at: string }>;

const landedKey = (e: Event) => (e.landed?.id ? `${e.landed.id}@${e.landed.version ?? 0}` : '');

/** The sub-change whose merge this landing records: the version was landed first by a sub-change of the change. */
function mergedFrom(e: Event, change: string, landings: Landings, members: Map<string, FamilyMember>): { change: string; seq: number } | undefined {
  if (e.op !== 'landed') return undefined;
  const first = landings.get(landedKey(e));
  return first && first.change !== change && members.get(first.change)?.parentId === change ? first : undefined;
}

function describe(e: Event, members: Map<string, FamilyMember>, merged?: { change: string }): Pick<HistoryItem, 'label' | 'kind'> {
  const p = patchOf(e);
  const from = e.draft?.inherited ? ' from the parent’s draft' : '';
  switch (e.op) {
    case 'created':
      return { label: `created${from}`, kind: from ? 'fork' : 'edit' };
    case 'checkedOut':
      return { label: `checked out${from}`, kind: from ? 'fork' : 'edit' };
    case 'updated': {
      if (p['resolved']) return { label: 'conflicts settled, kept as is', kind: 'edit' };
      const what = [
        ...Object.keys((p['props'] as object) ?? {}),
        ...((p['unset'] as string[]) ?? []),
        ...(p['ownerId'] ? ['owner'] : []),
        ...(p['addLink'] || p['removeLink'] || p['updateLink'] ? ['links'] : []),
      ];
      return { label: `edited ${what.join(', ')}`.trim(), kind: 'edit' };
    }
    case 'transitioned': {
      if (p['rebased']) {
        const conflicts = (p['conflicts'] as string[] | null) ?? [];
        return { label: `rebased onto the parent${conflicts.length ? ` — conflicts: ${conflicts.join(', ')}` : ''}`, kind: 'rebase' };
      }
      const integrated = p['integrated'] as { change?: string } | undefined;
      if (integrated) {
        const sub = members.get(integrated.change ?? '');
        return { label: `integrated from sub-change ${sub?.title || shortId(integrated.change)}`, kind: 'integrate' };
      }
      if (p['adopted']) return { label: 'installed by an adopted flow', kind: 'edit' };
      const st = p['state'] as { from?: string; to?: string } | undefined;
      return { label: st ? `moved from ${st.from || '?'} to ${st.to || '?'}` : 'moved', kind: 'edit' };
    }
    case 'reviewed':
      return { label: `${e.review?.status ?? 'reviewed'}${e.review?.comment ? ` — ${e.review.comment}` : ''}`, kind: 'review' };
    case 'rebased':
      return { label: 'rebased onto the parent’s branch', kind: 'rebase' };
    case 'landed':
      if (merged) {
        const sub = members.get(merged.change);
        return { label: `merged from sub-change ${sub?.title || shortId(merged.change)} as v${e.landed?.version ?? '?'}`, kind: 'integrate' };
      }
      return { label: `landed as v${e.landed?.version ?? '?'}${e.branch ? ` on ${e.branch}` : ''}`, kind: 'land' };
    default:
      return { label: e.op ?? '', kind: 'edit' };
  }
}

/**
 * The rows of the history graph of a family of changes, newest first. logs holds the `impact.` entries of each member.
 * key keeps the events of one node only (by its key); empty: every node.
 */
export function familyEntries(members: FamilyMember[], logs: Map<string, LogEntry[]>, key = ''): LaneEntry<HistoryItem>[] {
  const byId = new Map(members.map((m) => [m.id, m]));
  const items: { item: HistoryItem; event: Event }[] = [];
  // the first landing of each version: a sub-change lands its commits on its own branch, then its merge lands them in
  // the parent
  const landings: Landings = new Map();
  for (const m of members) {
    for (const l of logs.get(m.id) ?? []) {
      const e = decodeLogEntry<Event>(l);
      const k = landedKey(e);
      const seq = Number(l.seq ?? e.seq ?? 0);
      if (e.op !== 'landed' || !k || e.flow) continue;
      const had = landings.get(k);
      if (!had || (e.at ?? '') < had.at) landings.set(k, { change: m.id, seq, at: e.at ?? '' });
    }
  }
  const merges = new Map<string, { change: string; seq: number }>();
  for (const m of members) {
    const keys = new Map<string, string>();
    for (const l of logs.get(m.id) ?? []) {
      const e = decodeLogEntry<Event>(l);
      if (e.state?.id && e.state.key) keys.set(e.state.id, e.state.key);
      // the main flow, per change impact; a sub-change's hand-over is drawn on the parent's row that receives it
      if (e.flow || !e.impactId || e.op === 'integrated' || e.op === 'adopted') continue;
      const k = keys.get(e.impactId) ?? shortId(e.impactId);
      if (key && k !== key) continue;
      const seq = Number(l.seq ?? e.seq ?? 0);
      const merged = mergedFrom(e, m.id, landings, byId);
      if (merged) merges.set(entryId(m.id, seq), merged);
      items.push({
        event: e,
        item: {
          change: m.id,
          changeTitle: m.title || shortId(m.id),
          seq,
          at: e.at ?? '',
          op: e.op ?? '',
          impact: e.impactId,
          key: k,
          by: e.by,
          ...describe(e, byId, merged),
        },
      });
    }
  }
  const present = new Set(items.map((x) => entryId(x.item.change, x.item.seq)));
  const lane = (change: string) => items.filter((x) => x.item.change === change).sort((a, b) => a.item.seq - b.item.seq);
  const out: LaneEntry<HistoryItem>[] = [];
  for (const m of members) {
    const own = lane(m.id);
    own.forEach(({ item, event }, i) => {
      const parents: LaneEdge[] = [];
      const p = patchOf(event);
      if (i > 0) parents.push({ id: entryId(m.id, own[i - 1].item.seq) });
      // the fork: the parent's draft it copied, else the parent's last event before this lane starts
      const inherited = event.draft?.inherited;
      if (inherited?.changeId && present.has(entryId(inherited.changeId, inherited.seq ?? 0)) && (item.kind === 'fork' || i === 0)) {
        parents.push({ id: entryId(inherited.changeId, inherited.seq ?? 0), label: 'copied from the parent' });
      } else if (i === 0 && m.parentId && !own.some((x) => x.item.kind === 'fork')) {
        // no copy of a parent's draft in this lane: it starts from where the parent stood
        const before = lane(m.parentId)
          .filter((x) => x.item.at <= item.at)
          .at(-1);
        if (before) parents.push({ id: entryId(m.parentId, before.item.seq), dashed: true });
      }
      const rebased = p['rebased'] as { change?: string; seq?: number } | undefined;
      if (rebased?.change && present.has(entryId(rebased.change, rebased.seq ?? 0))) {
        parents.push({ id: entryId(rebased.change, rebased.seq ?? 0), label: 'rebase', dashed: true });
      }
      const integrated = p['integrated'] as { change?: string; impact?: string } | undefined;
      if (integrated?.change) {
        const last = lane(integrated.change)
          .filter((x) => x.item.impact === integrated.impact)
          .at(-1);
        if (last) parents.push({ id: entryId(integrated.change, last.item.seq), label: 'integrated' });
      }
      const merged = merges.get(entryId(m.id, item.seq));
      if (merged && present.has(entryId(merged.change, merged.seq))) parents.push({ id: entryId(merged.change, merged.seq), label: 'merged' });
      const seen = new Set<string>();
      out.push({
        id: entryId(m.id, item.seq),
        parents: parents.filter((x) => (seen.has(x.id) ? false : (seen.add(x.id), true))),
        lane: m.id,
        item,
      });
    });
  }
  return out.sort((a, b) => b.item.at.localeCompare(a.item.at) || (a.item.change === b.item.change ? b.item.seq - a.item.seq : 0));
}

/** The node keys a family's history mentions, sorted: the choices of its node filter. */
export function familyKeys(entries: LaneEntry<HistoryItem>[]): string[] {
  return [...new Set(entries.map((e) => e.item.key))].sort();
}
