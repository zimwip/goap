// The jobs of the person (ADR 0149), kept current by the job stream (`GET /api/jobs/events`, server-sent events read
// through fetch): a snapshot of the recent jobs when it connects, then every write of one. The stream reconnects with a
// backoff and a new snapshot; the tabs that start a job follow it here to its end (a download, a change to open).
import { SvelteMap } from 'svelte/reactivity';
import { errorMessage, jobResult, jobService, onTokenChange, RpcError, streamJobs, type Job, type JobStreamEvent } from '../api';
import { downloadBlob } from '../download';
import { isActive, isFinal, newer } from '../jobView';

/** The known jobs by id. */
export const knownJobs = new SvelteMap<string, Job>();

export const jobStream = $state({ connected: false, error: '', loaded: false });

const followers = new Map<string, ((j: Job) => void)[]>();
const endListeners = new Set<(j: Job) => void>();

/** Takes a state of a job (from the stream, or the answer that started it). */
export function takeJob(j: Job): void {
  const id = j.id ?? '';
  if (!id) return;
  const prev = knownJobs.get(id);
  if (!newer(prev, j)) return;
  knownJobs.set(id, j);
  if (!isFinal(j) || (prev && isFinal(prev))) return;
  const fns = followers.get(id);
  followers.delete(id);
  for (const fn of fns ?? []) fn(j);
  for (const fn of endListeners) fn(j);
}

/** Calls `fn` once the job ended (at once when it has). */
export function followJob(j: Job, fn: (j: Job) => void): void {
  takeJob(j);
  const cur = knownJobs.get(j.id ?? '') ?? j;
  if (isFinal(cur)) {
    fn(cur);
    return;
  }
  followers.set(j.id ?? '', [...(followers.get(j.id ?? '') ?? []), fn]);
}

/** The job, once it ended. */
export const jobEnded = (j: Job): Promise<Job> => new Promise((resolve) => followJob(j, resolve));

/** Calls `fn` whenever a job ends; returns the unsubscribe. */
export function onJobEnded(fn: (j: Job) => void): () => void {
  endListeners.add(fn);
  return () => endListeners.delete(fn);
}

/** The jobs, newest first. */
export function jobList(): Job[] {
  return [...knownJobs.values()].sort((a, b) => Date.parse(b.createdAt ?? '') - Date.parse(a.createdAt ?? '') || 0);
}

/** The jobs queued or running, newest first. */
export const activeJobs = (): Job[] => jobList().filter(isActive);

/** Saves the file a job wrote. */
export async function downloadJobResult(j: Job): Promise<void> {
  const { blob, filename } = await jobResult(j);
  downloadBlob(blob, filename);
}

/** Cancels a job; the stream brings its new state. */
export async function cancelJob(j: Job): Promise<void> {
  const { job } = await jobService.cancel(j.id ?? '');
  if (job) takeJob(job);
}

function take(e: JobStreamEvent) {
  if (e.type === 'job') {
    takeJob(e.job);
    return;
  }
  for (const j of e.jobs) takeJob(j);
  jobStream.loaded = true;
  // a followed job the snapshot no longer lists (older than its window) is read alone
  const listed = new Set(e.jobs.map((j) => j.id));
  for (const id of followers.keys()) {
    if (listed.has(id)) continue;
    jobService
      .get(id)
      .then(({ job }) => job && takeJob(job))
      .catch(() => undefined);
  }
}

/** Follows the job stream while signed in; returns the stop. */
export function startJobs(): () => void {
  let stopped = false;
  let ctl: AbortController | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let backoff = 1000;

  const connect = () => {
    clearTimeout(timer);
    ctl?.abort();
    if (stopped) return;
    const c = new AbortController();
    ctl = c;
    streamJobs(take, c.signal, {
      onOpen: () => {
        jobStream.connected = true;
        jobStream.error = '';
        backoff = 1000;
      },
    })
      .catch((e: unknown) => {
        if (c.signal.aborted) return;
        jobStream.error = errorMessage(e);
        // no job service behind the gateway: look again rarely
        if (e instanceof RpcError && (e.code === 'unimplemented' || e.status === 502 || e.status === 503)) backoff = 30_000;
      })
      .finally(() => {
        if (c !== ctl) return;
        jobStream.connected = false;
        if (stopped || c.signal.aborted) return;
        timer = setTimeout(connect, backoff);
        backoff = Math.min(backoff * 2, 30_000);
      });
  };

  const offToken = onTokenChange(() => {
    knownJobs.clear();
    jobStream.loaded = false;
    backoff = 1000;
    connect();
  });
  connect();
  return () => {
    stopped = true;
    offToken();
    clearTimeout(timer);
    ctl?.abort();
  };
}
