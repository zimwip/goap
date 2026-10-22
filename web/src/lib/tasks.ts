// The tasks of a person (ADR 0158): pure helpers over the answer of `ListAsks`, the asks of the open changes, each
// naming its change. A task is an ask (ADR 0117). It is the person's when they hold it (they accepted it, or an
// administrator of the project gave it to them), or when it is addressed to them alone (an ask with no role, owned by
// them) and nobody holds it. The other tasks they may answer are available to them, unless they turned them down or
// someone holds them. Lists: the person's tasks across changes, the ones available to them, the tasks of one change
// (the working one, one open in an editor), the tasks of a project.
import type { ChangeAsk } from './api';

/** The key of a task across changes: an ask's id is stable while it stands, within its change. */
export const taskKey = (a: ChangeAsk): string => `${a.changeId ?? ''}|${a.id ?? ''}`;

/** The task is addressed to the person alone (no role, owned by them). */
export const addressedTo = (a: ChangeAsk, me: string): boolean => !!me && !a.roles?.length && a.owner === me;

/** The task is the person's: they hold it, or it is addressed to them alone and nobody holds it. */
export function isMine(a: ChangeAsk, me: string): boolean {
  if (!me) return false;
  return a.assignee ? a.assignee === me : addressedTo(a, me);
}

/** Someone other than the person holds the task. */
export const heldByOther = (a: ChangeAsk, me: string): boolean => !!a.assignee && a.assignee !== me;

/** The task is available to the person: they may answer it, it is not theirs, nobody holds it and they did not turn it down. */
export const isAvailable = (a: ChangeAsk, me: string): boolean => !!a.mayAnswer && !isMine(a, me) && !a.assignee && !a.declined;

/** The person's tasks, across changes, the oldest first. */
export const myTasks = (asks: ChangeAsk[], me: string): ChangeAsk[] => asks.filter((a) => isMine(a, me)).sort(bySince);

/** The tasks the person may take, across changes, the oldest first. */
export const availableTasks = (asks: ChangeAsk[], me: string): ChangeAsk[] => asks.filter((a) => isAvailable(a, me)).sort(bySince);

/** The tasks of a change, the oldest first. */
export const tasksOf = (asks: ChangeAsk[], changeId: string): ChangeAsk[] => asks.filter((a) => a.changeId === changeId).sort(bySince);

/** The tasks of the changes of a project, grouped by change (the change waiting longest first). */
export function byChange(asks: ChangeAsk[], projectId?: string): { changeId: string; title: string; asks: ChangeAsk[] }[] {
  const groups = new Map<string, { changeId: string; title: string; asks: ChangeAsk[] }>();
  for (const a of [...asks].sort(bySince)) {
    if (projectId !== undefined && a.projectId !== projectId) continue;
    const id = a.changeId ?? '';
    let g = groups.get(id);
    if (!g) groups.set(id, (g = { changeId: id, title: a.changeTitle || id, asks: [] }));
    g.asks.push(a);
  }
  return [...groups.values()];
}

/** Who holds the task, for a line: "you", the holder, or "" when nobody does. */
export const holderLabel = (a: ChangeAsk, me: string): string => (!a.assignee ? '' : a.assignee === me ? 'you' : a.assignee);

function bySince(a: ChangeAsk, b: ChangeAsk): number {
  return (a.since ?? '').localeCompare(b.since ?? '');
}
