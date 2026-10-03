// The location is the only copy of where the user is: the active tab is `/<kind>?<param>=<value>…`, home is
// `/`. Nothing about it is kept in the browser, so a new sign-in starts at home, a reload or a shared link
// reopens the tab, and the browser's back / forward buttons walk through the tabs visited. The server must
// answer index.html for these paths (Vite does; `/goap.*`, `/api` and `/auth` are the gateway's).
import type { TabSpec } from './types';

/** The path and query of a tab (home when none). */
export function formatRoute(spec?: TabSpec): string {
  if (!spec) return '/';
  const q = new URLSearchParams(spec.params).toString();
  return `/${encodeURIComponent(spec.kind)}${q ? `?${q}` : ''}`;
}

/** The tab a location designates (undefined: home). */
export function parseRoute(path: string, search: string): TabSpec | undefined {
  const m = /^\/([^/]+)\/?$/.exec(path);
  if (!m) return undefined;
  return { kind: decodeURIComponent(m[1]), params: Object.fromEntries(new URLSearchParams(search)) };
}

export const currentRoute = (): TabSpec | undefined => parseRoute(location.pathname, location.search);

/** Points the address bar at a tab (a history entry, or replacing the current one); it does not fire `onRoute`. */
export function navigate(spec?: TabSpec, replace = false): void {
  const url = formatRoute(spec);
  if (location.pathname + location.search === url) return;
  if (replace) history.replaceState(null, '', url);
  else history.pushState(null, '', url);
}

/** Calls `fn` when the user moves through the history (back, forward). */
export function onRoute(fn: (spec?: TabSpec) => void): () => void {
  const h = () => fn(currentRoute());
  window.addEventListener('popstate', h);
  return () => window.removeEventListener('popstate', h);
}
