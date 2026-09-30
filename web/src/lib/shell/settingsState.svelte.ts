// Platform settings modal: one global dialog with its own section nav,
// opened from the status bar, the user menu, or any "manage…" shortcut.
export const settingsState = $state<{ open: boolean; section: string }>({
  open: false,
  section: 'preferences',
});

export function openSettings(section = 'preferences'): void {
  settingsState.section = section;
  settingsState.open = true;
}

export function closeSettings(): void {
  settingsState.open = false;
}
