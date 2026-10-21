// Logical URLs (ADR 0154): lnk:<source>/<type>/<key>[?v=n], what the graph stores and serves a reference to a node as
// (the owner unit and the project of a version or a change, §9). A leaf module: the readers that need a key (the
// session's checks, the tabs keyed by unit or project) take it from the URL here.

export const LNK_SCHEME = 'lnk:';

/** The parts of a logical URL (lnk.URL), undefined for a text that is none. */
export interface LnkUrl {
  source: string;
  type: string;
  key: string;
  version?: number;
}

/** Reads a logical URL (lnk.Parse): the key keeps its slashes, each segment percent-decoded. */
export function parseLnk(text: string | undefined): LnkUrl | undefined {
  if (!text?.startsWith(LNK_SCHEME)) return undefined;
  const [path, query] = text.slice(LNK_SCHEME.length).split('?', 2);
  const [source, type, ...rest] = path.split('/');
  if (!source || !type || !rest.length) return undefined;
  let key: string;
  try {
    key = rest.map(decodeURIComponent).join('/');
  } catch {
    return undefined;
  }
  if (!key) return undefined;
  const v = query ? Number(new URLSearchParams(query).get('v')) : 0;
  return v > 0 ? { source, type, key, version: v } : { source, type, key };
}

/** The key a reference names: the key of its URL, or the text itself when it is a bare key ('' for none). */
export const refKey = (ref: string | undefined): string => parseLnk(ref)?.key ?? ref ?? '';
