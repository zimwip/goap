// The tasks of a person (ADR 0158): pure helpers over the answer of `ListAsks`, the asks of the open changes, each
// naming its change. A task is an ask (ADR 0117); it is the person's when they accepted it, or when it is addressed to
// them alone (an ask with no role, owned by them), which counts as accepted. Lists: the person's tasks across changes,
// the tasks of one change (the working one, one open in an editor), the tasks of a project.
import type { ChangeAsk } from './api';

/** The key of a task across changes: an ask's id is stable while it stands, within its change. */
export const taskKey = (a: ChangeAsk): string => `${a.changeId ?? ''}|${a.id ?? ''}`;

/** The task is addressed to the person alone (no role, owned by them): it counts as accepted. */
export const addressedTo = (a: ChangeAsk, me: string): boolean => !!me && !a.roles?.length && a.owner === me;

/** The task is the person's: addressed to them alone, or accepted while they may answer it. */
export function isMine(a: ChangeAsk, me: string, accepted: ReadonlySet<string>): boolean {
  return addressedTo(a, me) || (!!a.mayAnswer && accepted.has(taskKey(a)));
}

/** The person may accept the task: they may answer it and it is not theirs already. */
export const acceptable = (a: ChangeAsk, me: string, accepted: ReadonlySet<string>): boolean => !!a.mayAnswer && !isMine(a, me, accepted);

/** The person's tasks, across changes, the oldest first. */
export const myTasks = (asks: ChangeAsk[], me: string, accepted: ReadonlySet<string>): ChangeAsk[] => asks.filter((a) => isMine(a, me, accepted)).sort(bySince);

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

/** The accepted tasks still standing: an accepted task that no longer stands (answered, its change closed) is dropped. */
export const standing = (accepted: string[], asks: ChangeAsk[]): string[] => {
  const keys = new Set(asks.map(taskKey));
  return accepted.filter((k) => keys.has(k));
};

function bySince(a: ChangeAsk, b: ChangeAsk): number {
  return (a.since ?? '').localeCompare(b.since ?? '');
}
