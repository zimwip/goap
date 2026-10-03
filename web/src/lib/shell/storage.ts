// Tolerant access to localStorage (private browsing, blocked storage…).
export function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return fallback;
    return { ...fallback, ...(JSON.parse(raw) as T) };
  } catch {
    return fallback;
  }
}

export function loadRaw(key: string): unknown {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as unknown) : undefined;
  } catch {
    return undefined;
  }
}

export function save(key: string, value: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // ignored
  }
}

/** What the browser keeps for the signed-in user (not for the browser): dropped when the session changes. */
const SESSION_KEYS = ['goap.ide.notifications', 'goap.ide.assistant'];

/** State kept by earlier versions, now in the URL or in memory: removed so it cannot come back. */
const LEGACY_KEYS = ['goap.ide.tabs', 'goap.project', 'goap.ide.expanded', 'goap.ide.baselineTool', 'goap.ide.algorithms.domain'];

function remove(keys: string[]): void {
  try {
    for (const k of keys) localStorage.removeItem(k);
  } catch {
    // ignored
  }
}

export const forgetSessionState = (): void => remove(SESSION_KEYS);
export const forgetLegacyState = (): void => remove(LEGACY_KEYS);
