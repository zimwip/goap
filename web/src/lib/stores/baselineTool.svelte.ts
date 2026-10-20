// The Baseline tool: its nav panel (BaselineExplorer) picks a namespace and opens a branch's history in a baseline
// tab of the editor area (views/editors/BaselineTab.svelte), pinned by default.
import { MAIN_BRANCH } from '../namespace';
import { graph } from '../api';
import { openTab } from '../shell/tabs.svelte';

interface BaselineToolState {
  /** the namespace the explorer lists */
  namespace: string;
  /** bumped after a branch/baseline mutation (create, merge, abandon, describe…) so both the nav panel and the
   * baseline tabs, with no props between them, know to refetch */
  reload: number;
}

export const baselineTool: BaselineToolState = $state({ namespace: '', reload: 0 });

export function selectNamespace(ns: string): void {
  baselineTool.namespace = ns;
}

export function refreshBranches(): void {
  baselineTool.reload++;
}

/** Opens the baseline tab of a branch (one per branch), showing a baseline of it ('' : its head). */
export function openBaseline(namespace: string, branch: string, baseline = ''): void {
  openTab({ kind: 'baseline', params: { namespace, branch: branch || MAIN_BRANCH, baseline } }, { pin: true });
}

/** Shows this baseline in the tab of its branch — for links elsewhere in the app (search, a change's starting /
 * resulting baseline, a run's baseline). */
export async function viewBaseline(id: string, namespace: string): Promise<void> {
  let branch = MAIN_BRANCH;
  try {
    const r = await graph.listBaselines(namespace, undefined, { ids: [id] });
    branch = r.baselines?.find((x) => x.id === id)?.branch || MAIN_BRANCH;
  } catch {
    // best effort: shows it on main
  }
  if (!baselineTool.namespace) selectNamespace(namespace);
  openBaseline(namespace, branch, id);
}
