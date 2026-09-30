// Token usage modal: one global dialog, separate from the settings (it is
// information about consumption, not a preference).
export const usageState = $state<{ open: boolean }>({ open: false });

export function openUsage(): void {
  usageState.open = true;
}

export function closeUsage(): void {
  usageState.open = false;
}
