// The tasks of the open changes (ADR 0158), read once for every list: the person's tasks across changes, the tasks of
// the working change, of the changes open in an editor and of the project. Read again when a run moves (live stream) or
// the list of changes does, while a view follows them. Working on a task makes its change the working change and
// enters change mode, so a task is always done in the view of its change. Accepting a task is the person's own: it is
// kept in their preferences (ADR 0038), not shared.
import { untrack } from 'svelte';
import { engine, errorMessage, type ChangeAsk } from '../api';
import { stamp, keyOf } from '../flux/signals.svelte';
import { processes } from './live.svelte';
import { me } from './session.svelte';
import { editPrefs, prefs } from './preferences.svelte';
import { workIn, workInId, working, isWorking } from './workingChange.svelte';
import { changes } from './catalog.svelte';
import { setMode } from '../shell/mode.svelte';
import { openTab } from '../shell/tabs.svelte';
import { myTasks, standing, taskKey } from '../tasks';

export const tasks = $state({ asks: [] as ChangeAsk[], loaded: false, error: '' });

/** The tasks the person accepted. */
export const accepted = (): ReadonlySet<string> => new Set(prefs.values.acceptedTasks);

/** The person's tasks across the open changes. */
export const mine = (): ChangeAsk[] => myTasks(tasks.asks, me(), accepted());

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
    // the accepted tasks answered since are forgotten
    const kept = standing(prefs.values.acceptedTasks, tasks.asks);
    if (prefs.loaded && kept.length !== prefs.values.acceptedTasks.length) void editPrefs({ acceptedTasks: kept });
  } catch (e) {
    if (signal?.aborted) return;
    tasks.error = errorMessage(e);
    tasks.loaded = true;
  }
}

/** Reads the tasks again now. */
export const reloadTasks = (): Promise<void> => load();

/** Accepts a task, or gives it back. */
export function accept(a: ChangeAsk, yes = true): void {
  const k = taskKey(a);
  const all = prefs.values.acceptedTasks.filter((x) => x !== k);
  if (yes) all.push(k);
  void editPrefs({ acceptedTasks: all });
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
