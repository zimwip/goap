// The export of the graph (ADR 0104 JSON snapshot, ADR 0111 OKF zip) runs as a job (ADR 0149): the graph writes the
// file while the job stream shows its progress, and the page that started it saves the file once the job is done.
import { errorMessage, graph, type Job } from './api';
import { notify } from './shell/workbench.svelte';
import { downloadJobResult, followJob } from './stores/jobs.svelte';

/** Starts the export of a namespace (the head of a branch, default main, or a commit) and saves its file when done. */
export async function exportGraph(req: { namespace: string; branch?: string; commitId?: string; format: 'json' | 'okf' }): Promise<Job> {
  const { job } = await graph.startGraphExport(req);
  notify(`Export of ${req.namespace} started: it shows under Jobs in the status bar.`, 'info');
  followJob(job, (j) => {
    if (j.status === 'done') downloadJobResult(j).catch((e: unknown) => notify(errorMessage(e), 'error'));
  });
  return job;
}
