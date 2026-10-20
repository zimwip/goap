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
  const state =
    payload.state && !payload.state.id
      ? { ...payload.state, id: l.subject }
      : payload.state;
  return { ...envelope, ...payload, ...(state ? { state } : {}) } as T;
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
  if (!l.type?.startsWith("impact.") || !Array.isArray(payload.impacts))
    return [decodeLogEntry<T>(l)];
  const { impacts, ...top } = payload as {
    impacts: Record<string, unknown>[];
  } & Record<string, unknown>;
  return impacts.map((p, n) => {
    const part = unhoist(top, p);
    return decodeLogEntry<T>({
      ...l,
      id: `${l.id}#${n}`,
      subject: l.subject || (part.impactId as string | undefined),
      payload: JSON.stringify(part),
    });
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
