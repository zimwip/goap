// Token usage modal: one global dialog, separate from the settings (it is
// information about consumption, not a preference).
export const usageState = $state<{ open: boolean; period: string }>({ open: false, period: '' });

/** Opens the dialog, on `period` (an id of `USAGE_PERIODS`) when given, else on the preferred period. */
export function openUsage(period = ''): void {
  usageState.period = period;
  usageState.open = true;
}

export function closeUsage(): void {
  usageState.open = false;
}
