// Which sign-in UI the gateway wants (ADR 0040): fetched once at boot, before any token exists.
import { authConfig } from '../api';

export const authState = $state({ mode: '', loaded: false });

export async function loadAuthConfig(): Promise<void> {
  try {
    authState.mode = (await authConfig()).authMode || 'none';
  } catch {
    authState.mode = 'none'; // the gateway is unreachable or has no /api/auth/config (older deploy): behave as before
  } finally {
    authState.loaded = true;
  }
}
