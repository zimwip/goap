// Attaching a change to the change integrating it, and detaching it (ADR 0106): what the change tab shows of its
// guardian chain and offers to attach or detach. Pure, but for the two calls at the bottom; the server stays the
// authority (the methodology of the parent integrates the change's, the project and unit, no cycle).
import { graph } from './api';
import type { Change } from './api';

const OPEN = ['draft', 'active'];

/** The guardian chain of a change, outermost first ("release-based › sdlc"); '' when it is its methodology alone. */
export function guardianChain(c: Pick<Change, 'guardians'>): string {
  const names = (c.guardians ?? []).map((g) => g.methodology ?? '').filter(Boolean);
  return names.length > 1 ? names.join(' › ') : '';
}

/** A root change still open may be attached to a change integrating it. */
export function attachable(c: Pick<Change, 'parentId' | 'status'>): boolean {
  return !c.parentId && OPEN.includes(c.status ?? '');
}

/** A sub-change still open may be detached from the change integrating it. */
export function detachable(c: Pick<Change, 'parentId' | 'status'>): boolean {
  return !!c.parentId && OPEN.includes(c.status ?? '');
}

/**
 * The changes a change may be attached to: open, in its namespace, of another methodology, neither itself nor one of
 * the changes under it. Whether that methodology integrates the change's is the server's to say.
 */
export function parentChoices(c: Change, all: Change[]): Change[] {
  const below = new Set<string>([c.id ?? '']);
  for (let grew = true; grew; ) {
    grew = false;
    for (const x of all) {
      if (x.id && x.parentId && below.has(x.parentId) && !below.has(x.id)) {
        below.add(x.id);
        grew = true;
      }
    }
  }
  return all
    .filter((x) => x.id && !below.has(x.id) && OPEN.includes(x.status ?? '') && (x.namespace ?? '') === (c.namespace ?? '') && x.methodology && x.methodology !== c.methodology)
    .sort((a, b) => (a.title ?? '').localeCompare(b.title ?? ''));
}

export async function attachTo(changeId: string, parentId: string): Promise<Change | undefined> {
  return (await graph.attachChange(changeId, parentId)).change;
}

export async function detach(changeId: string): Promise<Change | undefined> {
  return (await graph.detachChange(changeId)).change;
}
