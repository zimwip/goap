import type { Int64 } from "./types/common";
import type {
  ExecutionRecord,
  GraphNode,
  LogEntry,
  NodeRef,
} from "./types/graph";

/** Decodes a log entry's payload (ADR 0030) as T: a fact, a journal record or an impact event. An impact event's
 * payload leaves its envelope to the columns of the entry (ADR 0142): it is put back. */
export function decodeLogEntry<T>(l: LogEntry): T {
  const payload = JSON.parse(l.payload ?? "{}");
  if (l.type?.startsWith("fact.")) return decodeFact<T>(l, payload);
  if (!l.type?.startsWith("impact.")) return payload as T;
  const envelope = {
    id: l.id,
    changeId: l.changeId,
    seq: l.seq,
    impactId: l.subject || undefined,
    flow: l.flow,
    execution: l.execution,
    by: l.by,
    at: l.at,
  };
  // a declaration leaves to the entry its time, its author, the review it starts with and a creation's intent (ADR 0180)
  let state = payload.state
    ? {
        createdAt: l.at,
        producedBy: l.by ?? "",
        review: "proposed",
        ...(payload.op === "created" ? { intent: "created" } : {}),
        ...payload.state,
        ...(payload.state.id ? {} : { id: l.subject }),
      }
    : payload.state;
  const draft = payload.draft as
    | { node?: string; key?: string; type?: string; base?: { id?: string } }
    | undefined;
  // what the log leaves out of an event installing a draft (contracts/domain marshalImpact): its post, the draft
  // reference of its node ("post": null when it has none), and on a creation the key and type of its draft
  const node =
    draft && (payload.op === "created" || payload.op === "checkedOut")
      ? payload.fromBase
        ? draft.base?.id
        : draft.node
      : undefined;
  const post =
    node && !("post" in payload)
      ? { post: { id: node, version: 0 } }
      : payload.post === null
        ? { post: undefined }
        : {};
  if (state && payload.op === "created" && draft && !payload.fromBase)
    state = {
      ...state,
      ...("key" in state ? {} : { key: draft.key ?? "" }),
      ...("type" in state ? {} : { type: draft.type ?? "" }),
    };
  return {
    ...envelope,
    ...payload,
    ...post,
    ...(state ? { state } : {}),
  } as T;
}

/** A fact as its entry stores it (contracts/domain FactEntry, ADR 0180): its id, type, time and execution in the
 * columns, its author, flow and target too unless the payload writes them. */
function decodeFact<T>(l: LogEntry, payload: Record<string, unknown>): T {
  return {
    id: l.id,
    type: l.type?.slice("fact.".length),
    createdAt: l.at,
    ...(l.execution ? { execution: l.execution } : {}),
    ...(l.flow ? { flow: l.flow } : {}),
    ...(l.subject ? { target: l.subject } : {}),
    ...(l.by ? { producedBy: l.by } : {}),
    ...payload,
  } as T;
}

/** Puts back into a part of an entry what the parts held alike (ADR 0144: a key, or a key of an object every part held). */
function unhoist(
  top: Record<string, unknown>,
  part: Record<string, unknown>,
): Record<string, unknown> {
  const out = { ...part };
  for (const [k, v] of Object.entries(top)) {
    const own = out[k];
    if (own === undefined) out[k] = v;
    else if (own && typeof own === "object" && v && typeof v === "object")
      out[k] = unhoist(
        v as Record<string, unknown>,
        own as Record<string, unknown>,
      );
  }
  return out;
}

/** Decodes the impact events of a log entry: the one it records, or the several one entry carries (ADR 0144: the
 * verdicts of a review, the versions of a commit, as `impacts` parts), each with the envelope of the entry and the id
 * `<entry>#<n>`. Another entry is decoded as one value (decodeLogEntry). */
export function decodeImpactEvents<T>(l: LogEntry): T[] {
  const payload = JSON.parse(l.payload ?? "{}");
  if (l.type === "impact.committed" && Array.isArray(payload.nodes))
    return commitEvents<T>(l, payload);
  if (!l.type?.startsWith("impact.") || !Array.isArray(payload.impacts))
    return [decodeLogEntry<T>(l)];
  const { impacts, ...top } = payload as {
    impacts: (Record<string, unknown> | string)[];
  } & Record<string, unknown>;
  return impacts.map((p, n) => {
    // a part holding its impact only is the impact's id (the verdicts of one review)
    const part = unhoist(top, typeof p === "string" ? { impactId: p } : p);
    return decodeLogEntry<T>({
      ...l,
      id: `${l.id}#${n}`,
      subject: l.subject || (part.impactId as string | undefined),
      payload: JSON.stringify(part),
    });
  });
}

/** A commit is one event naming its nodes (ADR 0144 addendum: `{"op": "committed", "commit", "branch", "nodes"}`, the
 * graph serving each node with its impact and version): read as one committed event per node, as the other impact
 * events of an entry. */
function commitEvents<T>(
  l: LogEntry,
  payload: { commit?: string; branch?: string; nodes: unknown[] },
): T[] {
  const many = payload.nodes.length > 1;
  return payload.nodes.map((raw, n) => {
    const node = (typeof raw === "string" ? { id: raw } : raw) as {
      id?: string;
      impactId?: string;
      version?: number;
      effect?: unknown;
    };
    return {
      id: many ? `${l.id}#${n}` : l.id,
      changeId: l.changeId,
      seq: l.seq,
      impactId: node.impactId || (many ? undefined : l.subject || undefined),
      op: "committed",
      flow: l.flow,
      execution: l.execution,
      by: l.by,
      at: l.at,
      committed: { id: node.id, version: node.version },
      ...(node.effect ? { effect: node.effect } : {}),
      ...(payload.commit ? { commit: payload.commit } : {}),
      ...(payload.branch ? { branch: payload.branch } : {}),
    } as T;
  });
}

/** The execution journal records among log entries (ADR 0011 records, stored as journal.* entries, ADR 0030). */
export function executionsFromLog(entries: LogEntry[]): ExecutionRecord[] {
  return entries
    .filter((l) => l.type?.startsWith("journal."))
    .map((l) => decodeLogEntry<ExecutionRecord>(l));
}

/** Human-readable title of a node (`title` property, else `name`). */
export function nodeTitle(n: GraphNode | undefined): string {
  const t = n?.props?.["title"] ?? n?.props?.["name"];
  return typeof t === "string" ? t : "";
}

export function formatDate(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("fr-FR", { dateStyle: "short", timeStyle: "medium" });
}

/** Compares two version numbers "1.2.10" segment by segment (numerically when possible). */
export function compareVersions(
  a: string | undefined,
  b: string | undefined,
): number {
  const pa = (a ?? "").split(/[.-]/);
  const pb = (b ?? "").split(/[.-]/);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] ?? "";
    const y = pb[i] ?? "";
    const nx = Number(x);
    const ny = Number(y);
    const c =
      x !== "" && y !== "" && !Number.isNaN(nx) && !Number.isNaN(ny)
        ? nx - ny
        : x.localeCompare(y);
    if (c !== 0) return c;
  }
  return 0;
}

/** Increments the last numeric segment: 1.2.3 → 1.2.4. */
export function bumpPatch(version: string | undefined): string {
  const v = version ?? "";
  const m = /^(.*?)(\d+)(\D*)$/.exec(v);
  if (!m) return v ? `${v}.1` : "0.1.0";
  return `${m[1]}${Number(m[2]) + 1}${m[3]}`;
}

/** Numeric value of a proto3 integer (number or string for int64). */
export function int(v: Int64 | undefined | null): number {
  if (v === undefined || v === null || v === "") return 0;
  const n = typeof v === "number" ? v : Number(v);
  return Number.isFinite(n) ? n : 0;
}

/** 12345 → "12 345" (grouped thousands). */
export function formatInt(v: Int64 | undefined | null): string {
  return int(v).toLocaleString("fr-FR");
}

export function formatDuration(ms: Int64 | undefined | null): string {
  const n = int(ms);
  if (n < 1000) return `${n} ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(1)} s`;
  return `${Math.floor(n / 60_000)} min ${Math.round((n % 60_000) / 1000)} s`;
}

export function formatTime(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString("fr-FR", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function shortId(id: string | undefined): string {
  return id ? id.slice(0, 8) : "";
}

/** A reference to the draft a change holds of a node (ADR 0079): an id with no version. A node has no version while a
 * change works on it; the version is written when the change commits. The web never needs a version for a draft. */
export function isDraft(
  ref: NodeRef | undefined,
): ref is NodeRef & { id: string } {
  return !!ref?.id && !ref.version;
}

/** The draft reference of a node. */
export const draftRef = (id: string): NodeRef => ({ id, version: 0 });
