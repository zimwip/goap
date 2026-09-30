// Which sign-in UI the gateway wants (ADR 0040): fetched once at boot, before any token exists.
import { authConfig } from '../api';

export const authState = $state({ mode: '', loaded: false });

/**
 * Whether a mode is the platform's own sign-in (ADR 0040, 0042: "local", the default unless an external
 * identity provider is in place): the sign-in page, and signing out from the profile menu. An SSO mode
 * signs in and out through its provider instead.
 */
export const signsInLocally = (mode: string): boolean => mode === 'local';

export async function loadAuthConfig(): Promise<void> {
  try {
    authState.mode = (await authConfig()).authMode || 'none';
  } catch {
    authState.mode = 'none'; // the gateway is unreachable or has no /api/auth/config (older deploy): behave as before
  } finally {
    authState.loaded = true;
  }
}
