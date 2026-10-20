import { BASE, RpcError, getToken, reportUnauthorized, rpc } from './transport';
import type { JobJson } from '../../gen/goap/jobs/v1/jobs_pb';

const JOBS = 'goap.jobs.v1.JobService';

/** A long run of the platform (ADR 0149): an import from a source, a graph export or import. The service owning its
 *  kind starts it for the person who asked; a worker runs it and reports its progress; the job service streams it. */
export type Job = JobJson;

/** The job service (ADR 0149): a person reads, follows and cancels their own jobs (an administrator any). */
export const jobService = {
  get: (id: string, signal?: AbortSignal) => rpc<{ id: string }, { job?: Job }>(JOBS, 'GetJob', { id }, signal),
  /** The caller's jobs, newest first (`all`: every person's, administrators only). */
  list: (req: { kindPrefix?: string; target?: string; all?: boolean; limit?: number } = {}, signal?: AbortSignal) =>
    rpc<object, { jobs?: Job[] }>(JOBS, 'ListJobs', req, signal),
  /** Cancels a job: at once when queued, at the next report of its worker when running. */
  cancel: (id: string) => rpc<{ id: string }, { job?: Job }>(JOBS, 'CancelJob', { id }),
};

/** An event of the job stream (`GET /api/jobs/events`): `snapshot` first, with the caller's recent jobs, then `job`
 *  for every write of one. */
export type JobStreamEvent = { type: 'snapshot'; jobs: Job[] } | { type: 'job'; job: Job };

/**
 * A parser of server-sent events: fed the text as it arrives, it calls `onEvent` with each complete event (its name,
 * `message` by default, and its data, the `data:` lines joined); comments and `retry:` are skipped.
 */
export function sseParser(onEvent: (event: string, data: string) => void): (chunk: string) => void {
  let buf = '';
  let event = '';
  let data: string[] = [];
  return (chunk) => {
    buf += chunk;
    for (;;) {
      const nl = buf.search(/\r\n|\r|\n/);
      if (nl < 0) return;
      const line = buf.slice(0, nl);
      buf = buf.slice(nl + (buf.startsWith('\r\n', nl) ? 2 : 1));
      if (line === '') {
        if (data.length) onEvent(event || 'message', data.join('\n'));
        event = '';
        data = [];
        continue;
      }
      if (line.startsWith(':')) continue;
      const colon = line.indexOf(':');
      const field = colon < 0 ? line : line.slice(0, colon);
      let value = colon < 0 ? '' : line.slice(colon + 1);
      if (value.startsWith(' ')) value = value.slice(1);
      if (field === 'event') event = value;
      else if (field === 'data') data.push(value);
    }
  };
}

/** Reads one SSE body into job events. */
function decode(event: string, data: string): JobStreamEvent | undefined {
  try {
    const v = JSON.parse(data) as { jobs?: Job[] } & Job;
    if (event === 'snapshot') return { type: 'snapshot', jobs: v.jobs ?? [] };
    if (event === 'job') return { type: 'job', job: v };
  } catch {
    // a malformed event is skipped
  }
  return undefined;
}

/**
 * Follows the job stream until it ends or `signal` aborts (through fetch: an EventSource cannot send the token).
 * `onOpen` once the server answered. Rejects with an `RpcError` when the server refuses the stream; resolves when the
 * stream ends, which the caller answers by connecting again.
 */
export async function streamJobs(onEvent: (e: JobStreamEvent) => void, signal: AbortSignal, opts: { all?: boolean; onOpen?: () => void } = {}): Promise<void> {
  const headers: Record<string, string> = { Accept: 'text/event-stream' };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  let res: Response;
  try {
    res = await fetch(`${BASE}/api/jobs/events${opts.all ? '?all=1' : ''}`, { headers, signal, cache: 'no-store' });
  } catch (e) {
    if (signal.aborted) throw e;
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '');
    let message = text || res.statusText;
    try {
      message = (JSON.parse(text) as { message?: string }).message ?? message;
    } catch {
      // non-JSON body
    }
    reportUnauthorized(token, res.status, message);
    throw new RpcError(res.status === 401 ? 'unauthenticated' : res.status === 404 ? 'unimplemented' : 'unknown', message, res.status);
  }
  opts.onOpen?.();
  const feed = sseParser((event, data) => {
    const e = decode(event, data);
    if (e) onEvent(e);
  });
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      feed(value);
    }
  } finally {
    reader.releaseLock();
  }
}

/** The file a job wrote (`GET /api/jobs/<id>/result`), with the name it gave it. */
export async function jobResult(job: Job, signal?: AbortSignal): Promise<{ blob: Blob; filename: string }> {
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  let res: Response;
  try {
    res = await fetch(`${BASE}/api/jobs/${encodeURIComponent(job.id ?? '')}/result`, { headers, signal });
  } catch (e) {
    if (signal?.aborted) throw e;
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    reportUnauthorized(token, res.status, text);
    throw new RpcError(res.status === 404 ? 'not_found' : res.status === 401 ? 'unauthenticated' : 'unknown', text || res.statusText, res.status);
  }
  const name = /filename="([^"]*)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1];
  return { blob: await res.blob(), filename: name || job.result?.name || `job-${job.id}` };
}
