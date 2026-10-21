// How a job reads (ADR 0149): its kind, its progress, its counters, its timing. Pure, for the status bar, the Jobs tab
// and the tabs that start jobs.
import type { GraphImportResult, Job } from './api';

export const FINAL = ['done', 'failed', 'cancelled'];

/** The job is over (done, failed or cancelled): nothing will change it any more. */
export const isFinal = (j: Job | undefined): boolean => FINAL.includes(j?.status ?? '');
/** The job is queued or running. */
export const isActive = (j: Job | undefined): boolean => !!j && !isFinal(j);

/** How far the job is, 0 to 1, when its worker says how much there is to do; undefined otherwise. */
export function fraction(j: Job): number | undefined {
  const total = Number(j.total ?? 0);
  if (!(total > 0)) return j.status === 'done' ? 1 : undefined;
  return Math.min(1, Math.max(0, Number(j.done ?? 0) / total));
}

/** The kind of a job in words: "Import code (git)", "Graph export", "Graph import". */
export function kindLabel(kind: string | undefined): string {
  const k = kind ?? '';
  if (k.startsWith('import.')) {
    const [domain, importer] = k.slice('import.'.length).split('.');
    return importer ? `Import ${domain} (${importer})` : `Import ${domain}`;
  }
  if (k === 'graph.export') return 'Graph export';
  if (k === 'graph.import') return 'Graph import';
  return k;
}

/** The title of a job: its label, else its kind. */
export const jobTitle = (j: Job): string => j.label || kindLabel(j.kind);

/** The counters of a job, as a line ("12 commits, 40 created, 3 updated"); zeros left out. */
export function statsLine(j: Job): string {
  return Object.entries(j.stats ?? {})
    .filter(([, n]) => Number(n) > 0)
    .map(([k, n]) => `${n} ${k}`)
    .join(', ');
}

/** A duration in words: "12 s", "3 min 4 s", "1 h 2 min". */
export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
}

/** How long the job ran (or has run until `now`); empty while it is queued. */
export function elapsed(j: Job, now: number): string {
  const start = Date.parse(j.startedAt ?? '');
  if (Number.isNaN(start)) return '';
  const end = Date.parse(j.endedAt ?? '');
  return duration((Number.isNaN(end) ? now : end) - start);
}

/** The change a job created or wrote in (a graph import names it in its output). */
export function jobChange(j: Job): { id: string; title: string } | undefined {
  const o = (j.output ?? {}) as Record<string, unknown>;
  const id = typeof o.changeId === 'string' ? o.changeId : '';
  return id ? { id, title: typeof o.changeTitle === 'string' && o.changeTitle ? o.changeTitle : id } : undefined;
}

/** Is `next` a later state of the job than `prev`? An ended job never goes back; otherwise the latest update wins. */
export function newer(prev: Job | undefined, next: Job): boolean {
  if (!prev) return true;
  if (isFinal(prev) && !isFinal(next)) return false;
  const a = Date.parse(prev.updatedAt ?? '');
  const b = Date.parse(next.updatedAt ?? '');
  return Number.isNaN(a) || Number.isNaN(b) || b >= a;
}

/** A size in bytes in words. */
export function size(n: string | number | undefined): string {
  const b = Number(n ?? 0);
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KiB`;
  return `${(b / 1024 / 1024).toFixed(1)} MiB`;
}

/** What a graph import did (ADR 0104), from the output of its job: the change it created (none when the graph held the
 *  snapshot already), the counts and the warnings. */
export function graphImportResult(j: Job): GraphImportResult {
  const o = (j.output ?? {}) as Record<string, unknown>;
  const n = (k: string) => (typeof o[k] === 'number' ? (o[k] as number) : 0);
  const change = jobChange(j);
  return {
    ...(change ? { change: { id: change.id, title: change.title } } : {}),
    created: n('created'),
    updated: n('updated'),
    unchanged: n('unchanged'),
    warnings: Array.isArray(o.warnings) ? o.warnings.filter((w): w is string => typeof w === 'string') : [],
  };
}

/** How long the job still needs at the pace it went so far ("about 3 min left"); empty while unknown (no total, too
 *  early to tell, or ended). */
export function remaining(j: Job, now: number): string {
  if (!isActive(j)) return '';
  const total = Number(j.total ?? 0);
  const done = Number(j.done ?? 0);
  const start = Date.parse(j.startedAt ?? '');
  if (!(total > 0) || !(done > 0) || done >= total || Number.isNaN(start)) return '';
  const spent = now - start;
  if (spent < 3000) return '';
  return `about ${duration((spent / done) * (total - done))} left`;
}

/** The feedback lines of a job, oldest first, with their time of day. */
export function logLines(j: Job): { at: string; text: string }[] {
  return (j.log ?? []).map((l) => {
    const t = Date.parse(typeof l.at === 'string' ? l.at : '');
    return { at: Number.isNaN(t) ? '' : new Date(t).toLocaleTimeString(), text: l.text ?? '' };
  });
}

/** A time of a job in words, date and time of day; empty when unset. */
export function when(ts: string | undefined): string {
  const t = Date.parse(ts ?? '');
  return Number.isNaN(t) ? '' : new Date(t).toLocaleString();
}

/** What the detail of a job lists about it, in order, the unset facts left out. */
export function jobFacts(j: Job, now: number): { label: string; value: string }[] {
  const facts: [string, string | undefined][] = [
    ['Kind', kindLabel(j.kind)],
    ['Target', j.target],
    ['Started by', j.by],
    ['Project', j.projectId],
    ['Worker', j.worker],
    ['Created', when(j.createdAt)],
    ['Started', when(j.startedAt)],
    ['Ended', when(j.endedAt)],
    ['Duration', elapsed(j, now)],
    ['Id', j.id],
  ];
  return facts.filter(([, v]) => !!v).map(([label, value]) => ({ label, value: value as string }));
}

/** The parameters a job was started with, as name and value, a structured value as JSON. */
export function paramLines(j: Job): { name: string; value: string }[] {
  return Object.entries((j.params ?? {}) as Record<string, unknown>)
    .filter(([, v]) => v !== undefined && v !== null && v !== '')
    .map(([name, v]) => ({ name, value: typeof v === 'object' ? JSON.stringify(v) : String(v) }));
}
