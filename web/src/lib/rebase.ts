// A sub-change rebased onto its parent (ADR 0082): the names of the conflicts a rebase leaves, read for people.
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
