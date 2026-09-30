// Selection shared between the Baseline tool's nav panel (BaselineExplorer) and its editor-area workspace
// (BaselineWorkspace): siblings in the shell, so the selection lives here rather than as props.
import { load, save } from '../shell/storage';
import { MAIN_BRANCH } from '../namespace';

const KEY = 'goap.ide.baselineTool';

interface BaselineToolState {
  namespace: string;
  branch: string;
}

export const baselineTool: BaselineToolState = $state(load(KEY, { namespace: '', branch: MAIN_BRANCH }));

$effect.root(() => {
  $effect(() => save(KEY, { ...baselineTool }));
});

export function selectNamespace(ns: string): void {
  baselineTool.namespace = ns;
  baselineTool.branch = MAIN_BRANCH;
}

export function selectBranch(name: string): void {
  baselineTool.branch = name;
}
