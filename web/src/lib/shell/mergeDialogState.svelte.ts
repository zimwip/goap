// Merge dialog: one global instance hosting MergeResolver, opened from a branch's context menu (a merge can hit
// conflicts needing per-node resolution, so it can't be a single instant context-menu action).
export const mergeDialog = $state<{ open: boolean; namespace: string; from: string; into: string }>({
  open: false,
  namespace: '',
  from: '',
  into: '',
});

export function openMergeDialog(namespace: string, from: string, into: string): void {
  mergeDialog.namespace = namespace;
  mergeDialog.from = from;
  mergeDialog.into = into;
  mergeDialog.open = true;
}

export function closeMergeDialog(): void {
  mergeDialog.open = false;
}
