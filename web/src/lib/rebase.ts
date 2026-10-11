// A change rebased onto its parent (ADR 0082) or onto the head of its target branch (ADR 0101): the names of the conflicts a rebase leaves, read for people.
import { shortId } from './api';

/** A conflict name (props.<key>, owner, state, link:<type>:<node>, node) as a reader sees it. */
export function conflictLabel(name: string): string {
  if (name.startsWith('props.')) return `property “${name.slice('props.'.length)}”`;
  if (name === 'owner') return 'owner';
  if (name === 'state') return 'lifecycle state';
  if (name === 'node') return 'the parent rejected or withdrew this node';
  if (name.startsWith('link:')) {
    const rest = name.slice('link:'.length);
    const at = rest.lastIndexOf(':');
    return at < 0 ? `link ${rest}` : `link ${rest.slice(0, at)} → ${shortId(rest.slice(at + 1))}`;
  }
  return name;
}

/** "Behind <branch> (now at <onto>)", with the nodes changed there that this change works on. */
export function behindLine(branch: string, onto: string, keys: string[]): string {
  const base = `Behind ${branch || 'its target branch'} (now at ${shortId(onto)})`;
  return keys.length ? `${base}: ${keys.join(', ')} changed there since this change took ${keys.length > 1 ? 'them' : 'it'}.` : `${base}.`;
}

/** Whether the sync pane has anything to say: a sub-change behind its parent, a root change behind its branch, conflicts, an error. */
export function syncVisible(s: { isSub: boolean; behind: number; onto: string; conflicts: number; error: string }): boolean {
  return (s.isSub ? s.behind > 0 : !!s.onto) || s.conflicts > 0 || !!s.error;
}
