// The positions of the logs of the changes (ADR 0153): a change is read as a view of its log, at a position (its seq).
// A write of the graph answers where it left the log of each change it wrote (X-Goap-Change-Seq), an event of a change
// says it too (its version); the web keeps the highest position it knows per change and asks every later read of that
// change to reach it (X-Goap-At-Least), so a view never goes back to an older state than one it already showed or wrote.
// A read whose view is behind is refused (reason `behind`) and tried again by the transport.

export const CHANGE_SEQ_HEADER = 'X-Goap-Change-Seq';
export const AT_LEAST_HEADER = 'X-Goap-At-Least';

/** the changes remembered at most; the oldest noted are forgotten first */
export const MAX_POSITIONS = 500;

const known = new Map<string, number>();

/** Notes that the log of a change reached `seq` (a lower position than the one known is ignored). */
export function notePosition(id: string, seq: number | string | undefined | null): void {
  const n = Number(seq ?? 0);
  if (!id || !Number.isFinite(n) || n <= 0 || n <= (known.get(id) ?? 0)) return;
  known.delete(id); // the latest noted last, so the oldest go first
  known.set(id, n);
  while (known.size > MAX_POSITIONS) known.delete(known.keys().next().value as string);
}

/** The position known for a change, 0 when none. */
export const positionOf = (id: string): number => known.get(id) ?? 0;

/** Forgets every position (tests, a new session). */
export function resetPositions(): void {
  known.clear();
}

/** Reads a header value: `<change>:<seq>` pairs separated by commas, malformed ones left out. */
export function parsePositions(v: string | null | undefined): Map<string, number> {
  const out = new Map<string, number>();
  for (const p of (v ?? '').split(',')) {
    const i = p.lastIndexOf(':');
    if (i <= 0) continue;
    const seq = Number(p.slice(i + 1).trim());
    if (Number.isInteger(seq) && seq > 0) out.set(p.slice(0, i).trim(), seq);
  }
  return out;
}

/** Notes the positions a header value names. */
export function notePositions(v: string | null | undefined): void {
  for (const [id, seq] of parsePositions(v)) notePosition(id, seq);
}

/** the fields of a request naming a change */
const CHANGE_FIELDS = ['changeId', 'id', 'parentId'] as const;

/**
 * The value of X-Goap-At-Least for a request: the known positions of the changes its body names (`changeId`, `id`,
 * `parentId`), undefined when it names none known. A name that is no change is never known, so it adds nothing.
 */
export function atLeastFor(body: unknown): string | undefined {
  if (!body || typeof body !== 'object') return undefined;
  const parts: string[] = [];
  for (const f of CHANGE_FIELDS) {
    const id = (body as Record<string, unknown>)[f];
    if (typeof id !== 'string') continue;
    const seq = positionOf(id);
    if (seq > 0 && !parts.some((p) => p.startsWith(`${id}:`))) parts.push(`${id}:${seq}`);
  }
  return parts.length ? parts.join(',') : undefined;
}
