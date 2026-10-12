// A change rebased onto the head of its target branch (ADR 0101; a sub-change: its parent's branch, ADR 0103): the names of the conflicts a rebase leaves, read for people.
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

/** Whether the sync pane has anything to say: the change is behind its target branch, conflicts, an error. */
export function syncVisible(s: { onto: string; conflicts: number; error: string }): boolean {
  return !!s.onto || s.conflicts > 0 || !!s.error;
}

/** What a "rebase" task of a run says conflicts, one line per impact: "REQ-1: property “title”, owner". */
export function conflictLines(impacts: { key?: string; impact?: string; conflicts?: string[] }[]): string[] {
  return impacts.map((i) => `${i.key || shortId(i.impact ?? '')}: ${(i.conflicts ?? []).map(conflictLabel).join(', ')}`);
}

/** Where the change was rebased onto, as the task shows it. */
export function rebasedOnto(t: { branch?: string; onto?: string }): string {
  if (t.branch && t.onto) return `${t.branch} (${shortId(t.onto)})`;
  return t.branch || (t.onto ? shortId(t.onto) : 'its target');
}
