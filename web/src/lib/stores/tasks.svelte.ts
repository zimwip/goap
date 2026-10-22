// The tasks of the open changes (ADR 0158), read once for every list: the person's tasks across changes, the tasks of
// the working change, of the changes open in an editor and of the project. Read again when a run moves (live stream) or
// the list of changes does, while a view follows them. Working on a task makes its change the working change and
// enters change mode, so a task is always done in the view of its change. Who holds a task is the engine's (accepted,
// turned down, given by an administrator of the project): every one sees it.
import { untrack } from 'svelte';
import { engine, errorMessage, type ChangeAsk } from '../api';
import { stamp, keyOf } from '../flux/signals.svelte';
import { processes } from './live.svelte';
import { me } from './session.svelte';
import { workIn, workInId, working, isWorking } from './workingChange.svelte';
import { changes } from './catalog.svelte';
import { setMode } from '../shell/mode.svelte';
import { openTab } from '../shell/tabs.svelte';
import { availableTasks, myTasks } from '../tasks';

export const tasks = $state({ asks: [] as ChangeAsk[], loaded: false, error: '' });

/** The person's tasks across the open changes. */
export const mine = (): ChangeAsk[] => myTasks(tasks.asks, me());

/** The tasks the person may take across the open changes. */
export const available = (): ChangeAsk[] => availableTasks(tasks.asks, me());

let followers = 0;
let stop: (() => void) | undefined;

/** Follows the tasks while a view shows them; returns the end of the following. */
export function followTasks(): () => void {
  if (followers++ === 0)
    stop = $effect.root(() => {
      // a run moved, or the changes did: read the asks again (once for a burst)
      $effect(() => {
        for (const p of processes.values()) void `${p.status}:${p.updatedAt}`;
        void stamp(keyOf.changes);
        void me();
        const ctl = new AbortController();
        const t = setTimeout(() => void untrack(() => load(ctl.signal)), 300);
        return () => (clearTimeout(t), ctl.abort());
      });
    });
  return () => {
    if (--followers === 0) stop?.();
  };
}

async function load(signal?: AbortSignal): Promise<void> {
  try {
    const r = await engine.listAsks('', signal);
    tasks.asks = r.asks ?? [];
    tasks.error = '';
    tasks.loaded = true;
  } catch (e) {
    if (signal?.aborted) return;
    tasks.error = errorMessage(e);
    tasks.loaded = true;
  }
}

/** Reads the tasks again now. */
export const reloadTasks = (): Promise<void> => load();

/** Lays what a write of a task left over the list, until the asks are read again (the run's event follows). */
function patch(a: ChangeAsk, set: Partial<ChangeAsk>): void {
  tasks.asks = tasks.asks.map((x) => (x.changeId === a.changeId && x.id === a.id ? { ...x, ...set } : x));
}

/** Accepts a task: it becomes the person's, the others no longer answer it. */
export async function accept(a: ChangeAsk): Promise<void> {
  await engine.acceptTask(a.processId ?? '');
  patch(a, { assignee: me(), assignedBy: me(), assignedAt: new Date().toISOString(), mayAccept: false, declined: false });
}

/** Turns a task down: given back by its holder, declined by one it is offered to; it is no longer offered to them. */
export async function reject(a: ChangeAsk, reason = ''): Promise<void> {
  await engine.rejectTask(a.processId ?? '', reason);
  patch(a, { assignee: '', assignedBy: '', assignedAt: '', declined: true });
}

/** Gives a task to someone (empty: nobody holds it), as an administrator of its project. */
export async function assign(a: ChangeAsk, subject: string): Promise<void> {
  await engine.assignTask(a.processId ?? '', subject);
  patch(a, { assignee: subject, assignedBy: subject ? me() : '', assignedAt: subject ? new Date().toISOString() : '', mayAccept: !subject && !!a.mayAnswer });
}

/** Opens the screen of a task: who it is for, who holds it, accept, turn down, give to someone. */
export function openTask(a: ChangeAsk, pin = false): void {
  openTab({ kind: 'task', params: { change: a.changeId ?? '', id: a.id ?? '' } }, { pin });
}

/** Works on a task: its change becomes the working change, the front enters change mode on the change, opened on the task. */
export async function workOnTask(a: ChangeAsk): Promise<boolean> {
  const id = a.changeId ?? '';
  if (!id) return false;
  if (!isWorking(id)) {
    const known = changes.items.find((c) => c.id === id);
    if (!(await (known ? workIn(known) : workInId(id)))) return false;
  }
  if (working.change?.id !== id || !setMode('change')) return false;
  openTab({ kind: 'change', params: { id, task: a.id ?? '' } }, { pin: true });
  return true;
}
