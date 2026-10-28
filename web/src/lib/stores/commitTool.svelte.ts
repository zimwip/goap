// The Commit tool: its nav panel (HistoryExplorer) picks a namespace and opens a branch's history in a commit
// tab of the editor area (views/editors/CommitTab.svelte), pinned by default.
import { MAIN_BRANCH } from '../namespace';
import { graph } from '../api';
import { openTab } from '../shell/tabs.svelte';

interface CommitToolState {
  /** the namespace the explorer lists */
  namespace: string;
  /** bumped after a branch/commit mutation (create, merge, abandon, describe…) so both the nav panel and the
   * commit tabs, with no props between them, know to refetch */
  reload: number;
}

export const commitTool: CommitToolState = $state({ namespace: '', reload: 0 });

export function selectNamespace(ns: string): void {
  commitTool.namespace = ns;
}

export function refreshBranches(): void {
  commitTool.reload++;
}

/** Opens the commit tab of a branch (one per branch), showing a commit of it ('' : its head). */
export function openCommit(namespace: string, branch: string, commit = ''): void {
  openTab({ kind: 'commit', params: { namespace, branch: branch || MAIN_BRANCH, commit } }, { pin: true });
}

/** Shows this commit in the tab of its branch — for links elsewhere in the app (search, a change's starting /
 * resulting commit, a run's commit). */
export async function viewCommit(id: string, namespace: string): Promise<void> {
  let branch = MAIN_BRANCH;
  try {
    const r = await graph.listCommits(namespace, undefined, { ids: [id] });
    branch = r.commits?.find((x) => x.id === id)?.branch || MAIN_BRANCH;
  } catch {
    // best effort: shows it on main
  }
  if (!commitTool.namespace) selectNamespace(namespace);
  openCommit(namespace, branch, id);
}
