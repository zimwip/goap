// The history of a change and its sub-changes as one branch graph: a lane per change, a row per event of the impact log of
// its main flow, and the edges that tie the lanes. A sub-change works on its own branch forked from its parent's (ADR
// 0103): its lane starts from where the parent stood, a rebase moves it onto the parent's branch, and its merge commits its
// commits in the parent (the parent's `committed` event of the same version).
import type { LaneEntry, LaneEdge } from "./components/HistoryGraph.svelte";
import {
  decodeImpactEvents,
  shortId,
  type ImpactEvent,
  type LogEntry,
} from "./api";

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
  kind: "edit" | "review" | "rebase" | "integrate" | "commit";
}

type Event = ImpactEvent;

const entryId = (change: string, seq: number) => `${change}:${seq}`;
/** The place of the n-th event of a log entry in its lane: an entry may carry several (ADR 0144). */
const partSeq = (l: LogEntry, n: number) => Number(l.seq ?? 0) + n / 1000;

function patchOf(e: Event): Record<string, unknown> {
  return (e.patch ?? {}) as Record<string, unknown>;
}

/** The change and log position that first committed a version, by `<node>@<version>`. */
type Commits = Map<string, { change: string; seq: number; at: string }>;

const committedKey = (e: Event) =>
  e.committed?.id ? `${e.committed.id}@${e.committed.version ?? 0}` : "";

/** The sub-change whose merge this commit records: the version was committed first by a sub-change of the change. */
function mergedFrom(
  e: Event,
  change: string,
  commits: Commits,
  members: Map<string, FamilyMember>,
): { change: string; seq: number } | undefined {
  if (e.op !== "committed") return undefined;
  const first = commits.get(committedKey(e));
  return first &&
    first.change !== change &&
    members.get(first.change)?.parentId === change
    ? first
    : undefined;
}

function describe(
  e: Event,
  members: Map<string, FamilyMember>,
  merged?: { change: string },
): Pick<HistoryItem, "label" | "kind"> {
  const p = patchOf(e);
  switch (e.op) {
    case "created":
      return { label: "created", kind: "edit" };
    case "checkedOut":
      return { label: "checked out", kind: "edit" };
    case "updated": {
      if (p["resolved"])
        return { label: "conflicts settled, kept as is", kind: "edit" };
      const what = [
        ...Object.keys((p["props"] as object) ?? {}),
        ...((p["unset"] as string[]) ?? []),
        ...(p["ownerId"] ? ["owner"] : []),
        ...(p["addLink"] || p["removeLink"] || p["updateLink"]
          ? ["links"]
          : []),
      ];
      return { label: `edited ${what.join(", ")}`.trim(), kind: "edit" };
    }
    case "transitioned": {
      if (p["rebased"]) {
        const conflicts = (p["conflicts"] as string[] | null) ?? [];
        return {
          label: `rebased onto the parent${conflicts.length ? ` — conflicts: ${conflicts.join(", ")}` : ""}`,
          kind: "rebase",
        };
      }
      if (p["adopted"])
        return { label: "installed by an adopted flow", kind: "edit" };
      const st = p["state"] as { from?: string; to?: string } | undefined;
      return {
        label: st ? `moved from ${st.from || "?"} to ${st.to || "?"}` : "moved",
        kind: "edit",
      };
    }
    case "reviewed":
      return {
        label: `${e.review?.status ?? "reviewed"}${e.review?.comment ? ` — ${e.review.comment}` : ""}`,
        kind: "review",
      };
    case "rebased":
      return { label: "rebased onto the parent’s branch", kind: "rebase" };
    case "committed":
      if (merged) {
        const sub = members.get(merged.change);
        return {
          label: `merged from sub-change ${sub?.title || shortId(merged.change)} as v${e.committed?.version ?? "?"}`,
          kind: "integrate",
        };
      }
      return {
        label: `committed as v${e.committed?.version ?? "?"}${e.branch ? ` on ${e.branch}` : ""}`,
        kind: "commit",
      };
    default:
      return { label: e.op ?? "", kind: "edit" };
  }
}

/**
 * The rows of the history graph of a family of changes, newest first. logs holds the `impact.` entries of each member.
 * key keeps the events of one node only (by its key); empty: every node.
 */
export function familyEntries(
  members: FamilyMember[],
  logs: Map<string, LogEntry[]>,
  key = "",
): LaneEntry<HistoryItem>[] {
  const byId = new Map(members.map((m) => [m.id, m]));
  const items: { item: HistoryItem; event: Event }[] = [];
  // the first commit of each version: a sub-change commits its versions on its own branch, then its merge commits them in
  // the parent
  const commits: Commits = new Map();
  for (const m of members) {
    for (const l of logs.get(m.id) ?? []) {
      for (const [n, e] of decodeImpactEvents<Event>(l).entries()) {
        const k = committedKey(e);
        const seq = partSeq(l, n);
        if (e.op !== "committed" || !k || e.flow) continue;
        const had = commits.get(k);
        if (!had || (e.at ?? "") < had.at)
          commits.set(k, { change: m.id, seq, at: e.at ?? "" });
      }
    }
  }
  const merges = new Map<string, { change: string; seq: number }>();
  for (const m of members) {
    const keys = new Map<string, string>();
    for (const l of logs.get(m.id) ?? [])
      for (const [n, e] of decodeImpactEvents<Event>(l).entries()) {
        if (e.state?.id && e.state.key) keys.set(e.state.id, e.state.key);
        // the main flow, per change impact; a sub-change's hand-over is drawn on the parent's row that receives it
        if (e.flow || !e.impactId || e.op === "adopted") continue;
        const k = keys.get(e.impactId) ?? shortId(e.impactId);
        if (key && k !== key) continue;
        const seq = partSeq(l, n);
        const merged = mergedFrom(e, m.id, commits, byId);
        if (merged) merges.set(entryId(m.id, seq), merged);
        items.push({
          event: e,
          item: {
            change: m.id,
            changeTitle: m.title || shortId(m.id),
            seq,
            at: e.at ?? "",
            op: e.op ?? "",
            impact: e.impactId,
            key: k,
            by: e.by,
            ...describe(e, byId, merged),
          },
        });
      }
  }
  const present = new Set(items.map((x) => entryId(x.item.change, x.item.seq)));
  const lane = (change: string) =>
    items
      .filter((x) => x.item.change === change)
      .sort((a, b) => a.item.seq - b.item.seq);
  const out: LaneEntry<HistoryItem>[] = [];
  for (const m of members) {
    const own = lane(m.id);
    own.forEach(({ item, event }, i) => {
      const parents: LaneEdge[] = [];
      if (i > 0) parents.push({ id: entryId(m.id, own[i - 1].item.seq) });
      // the fork: a lane starts from where the parent stood
      if (i === 0 && m.parentId) {
        const before = lane(m.parentId)
          .filter((x) => x.item.at <= item.at)
          .at(-1);
        if (before)
          parents.push({
            id: entryId(m.parentId, before.item.seq),
            dashed: true,
          });
      }
      const merged = merges.get(entryId(m.id, item.seq));
      if (merged && present.has(entryId(merged.change, merged.seq)))
        parents.push({
          id: entryId(merged.change, merged.seq),
          label: "merged",
        });
      const seen = new Set<string>();
      out.push({
        id: entryId(m.id, item.seq),
        parents: parents.filter((x) =>
          seen.has(x.id) ? false : (seen.add(x.id), true),
        ),
        lane: m.id,
        item,
      });
    });
  }
  return out.sort(
    (a, b) =>
      b.item.at.localeCompare(a.item.at) ||
      (a.item.change === b.item.change ? b.item.seq - a.item.seq : 0),
  );
}

/** The node keys a family's history mentions, sorted: the choices of its node filter. */
export function familyKeys(entries: LaneEntry<HistoryItem>[]): string[] {
  return [...new Set(entries.map((e) => e.item.key))].sort();
}
