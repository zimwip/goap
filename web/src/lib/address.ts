// Addresses (ADR 0171 §4-5): an address is opened the way the type of source of its scheme says. The graph resolves it
// (GraphService.ResolveAddress: a logical URL through the sources the project maps, a web address for the browser) and
// answers how to open the target; this module turns that answer into what the interface does. There is no file scheme:
// a local file is reached through a declared source (lnk:<source>/File/<path>).
import { graph, type AddressTarget } from './api';

/** What the interface does with a target opened. */
export type Opening =
  | { kind: 'node'; node: { id: string; key: string; type: string; namespace: string } }
  | { kind: 'tab'; href: string }
  | { kind: 'text'; title: string; language: string; content: string; truncated: boolean }
  | { kind: 'image'; title: string; href: string; media: string }
  | { kind: 'path'; title: string; href: string; media: string };

/** Reports a text that reads as an address: a scheme, then something (lnk:alm/Requirement/R-1, https://...). */
export function isAddress(s: unknown): s is string {
  return typeof s === 'string' && /^[a-z][a-z0-9+.-]*:\S+$/i.test(s.trim());
}

/** The scheme of an address, lower case. */
export const schemeOf = (s: string): string => s.trim().slice(0, Math.max(0, s.trim().indexOf(':'))).toLowerCase();

/** How to open a target the graph resolved: undefined when its source says nothing (a target only resolved). */
export function openingOf(t: AddressTarget): Opening | undefined {
  const o = t.open;
  const title = t.title || t.key || t.url || '';
  switch (o?.mode) {
    case 'node': {
      const type = t.type ?? '';
      const at = type.indexOf('@');
      return t.node?.id ? { kind: 'node', node: { id: t.node.id, key: t.key ?? '', type, namespace: at > 0 ? type.slice(0, at) : '' } } : undefined;
    }
    case 'external':
      return o.href && /^https?:\/\//i.test(o.href) ? { kind: 'tab', href: o.href } : undefined;
    case 'text':
      return { kind: 'text', title, language: o.language ?? '', content: t.content ?? '', truncated: !!o.truncated };
    case 'inline':
      // only the data: address of an image: nothing the page would run
      return o.href?.startsWith('data:image/') ? { kind: 'image', title, href: o.href, media: o.media ?? '' } : undefined;
    case 'path':
      return { kind: 'path', title, href: o.href ?? t.location ?? '', media: o.media ?? '' };
  }
  return undefined;
}

/** Resolves an address for the active project (or the one given) and says how to open it. */
export async function openingOfAddress(address: string, projectId = '', signal?: AbortSignal): Promise<Opening> {
  const res = await graph.resolveAddress(address.trim(), projectId, signal);
  const opening = res.target ? openingOf(res.target) : undefined;
  if (!opening) throw new Error(`${address}: its source tells no way to open it`);
  return opening;
}
