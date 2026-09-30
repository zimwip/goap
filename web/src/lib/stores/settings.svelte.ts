// Saving and discarding the edits of the settings dialog. What the dialog edits about the platform is graph data
// (platform or organisation namespace) staged in personal changes (stores/pending.svelte.ts); this module applies or
// removes them as a whole. The personal preferences are not part of it: they are saved as they change (ADR 0038).
import { confirmDialog } from '../shell/confirmState.svelte';
import { discardPending, loadPending, savePending } from './pending.svelte';
import { loadPrefs } from './preferences.svelte';

/** Reads the pending graph edits of the user and their personal preferences. Called at start and when the identity changes. */
export async function loadSettingsState(): Promise<void> {
  await Promise.all([loadPending(), loadPrefs()]);
}

/** Applies every pending edit. */
export async function saveSettings(): Promise<boolean> {
  return savePending();
}

/** Removes every pending edit; the saved values come back. */
export async function discardSettings(): Promise<boolean> {
  return discardPending();
}

/**
 * Asks before leaving with unsaved edits. Accepting loses them (their changes are discarded and removed); declining
 * keeps the user where they are. Resolves true when it is fine to leave.
 */
export async function confirmLeaveSettings(pendingCount: number): Promise<boolean> {
  if (!pendingCount) return true;
  const ok = await confirmDialog({
    title: 'Unsaved settings',
    message: `${pendingCount} change${pendingCount > 1 ? 's are' : ' is'} not saved. If you leave now ${pendingCount > 1 ? 'they' : 'it'} will be lost and the pending change removed. Lose ${pendingCount > 1 ? 'them' : 'it'}?`,
    confirmLabel: 'Lose my changes',
    cancelLabel: 'Keep editing',
    danger: true,
  });
  return ok ? await discardSettings() : false;
}
