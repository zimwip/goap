// Selection shared between the Baseline tool's nav panel (BaselineExplorer) and its editor-area workspace
// (BaselineWorkspace): siblings in the shell, so the selection lives here rather than as props.
import { MAIN_BRANCH } from '../namespace';
import { graph } from '../api';
import { showTool } from '../shell/layout.svelte';

interface BaselineToolState {
  namespace: string;
  branch: string;
  /** '' : the branch's head */
  baseline: string;
  /** bumped after a branch/baseline mutation (create, merge, abandon, describe…) so both the nav panel and the
   * workspace, siblings with no props between them, know to refetch */
  reload: number;
}

export const baselineTool: BaselineToolState = $state({ namespace: '', branch: MAIN_BRANCH, baseline: '', reload: 0 });

export function selectNamespace(ns: string): void {
  baselineTool.namespace = ns;
  baselineTool.branch = MAIN_BRANCH;
  baselineTool.baseline = '';
}

export function selectBranch(name: string): void {
  baselineTool.branch = name;
  baselineTool.baseline = '';
}

export function selectBaseline(id: string): void {
  baselineTool.baseline = id;
}

export function refreshBranches(): void {
  baselineTool.reload++;
}

/** Switches to the Baseline tool and shows this baseline (its namespace, branch, and itself specifically) —
 * for links elsewhere in the app (search, a change's starting/resulting baseline, a run's baseline). */
export async function viewBaseline(id: string, namespace: string): Promise<void> {
  selectNamespace(namespace);
  try {
    const r = await graph.listBaselines(namespace);
    const b = r.baselines?.find((x) => x.id === id);
    if (b?.branch) baselineTool.branch = b.branch;
  } catch {
    // best effort: leaves the selection on main
  }
  baselineTool.baseline = id;
  showTool('left', 'baselines');
}
