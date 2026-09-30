// Platform status modal: one global dialog, opened from the header's live
// indicator (merges event-stream state and platform health into one place).
export const platformStatusState = $state<{ open: boolean }>({ open: false });

export function openPlatformStatus(): void {
  platformStatusState.open = true;
}

export function closePlatformStatus(): void {
  platformStatusState.open = false;
}
