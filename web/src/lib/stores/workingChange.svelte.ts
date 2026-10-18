// The working change: the change the person works in, within the active project. The change editor opens as many
// changes as one likes to look around; working is done in one of them, selected from the header or from the change
// editor ("Work in this change"), or by the assistant (open_change, create_change). Selecting a change of another
// project makes that project the active one first; switching project leaves the working change of the project left
// and takes back the one last selected in the new project. It is not graph data: the preferences service keeps the
// last working change of each project (ADR 0038), so a reload or another device finds it again. The assistant reads
// it from the context of each turn (app.change, ADR 0092): it is the change its turns work on.
import { untrack } from 'svelte';
import { graph, errorMessage, type Change } from '../api';
import { notify } from '../shell/workbench.svelte';
import { editPrefs, prefs } from './preferences.svelte';
import { project, selectProject } from './project.svelte';
import { rootProject } from './session.svelte';

export interface WorkingChange {
  id: string;
  title: string;
  /** the project of the change (the root project's key when it names none) */
  projectId: string;
  namespace: string;
  status: string;
}

export const working = $state({ change: undefined as WorkingChange | undefined });

/** A change is worked in until it lands or is abandoned. */
export const workable = (c?: Pick<Change, 'status'>): boolean => !!c && c.status !== 'applied' && c.status !== 'abandoned';

const asWorking = (c: Change): WorkingChange => ({
  id: c.id ?? '',
  title: c.title || c.id || '',
  projectId: c.projectId || rootProject(),
  namespace: c.namespace ?? '',
  status: c.status ?? '',
});

/** Whether a change is the working change. */
export const isWorking = (id?: string): boolean => !!id && working.change?.id === id;

/** Remembers the working change of a project in the preferences ('' forgets it). */
function remember(projectKey: string, id: string): void {
  if (!projectKey) return;
  const all = { ...prefs.values.workingChanges };
  if ((all[projectKey] ?? '') === id) return;
  if (id) all[projectKey] = id;
  else delete all[projectKey];
  void editPrefs({ workingChanges: all });
}

/**
 * Makes a change the working change, switching to its project first when it is another; false when the change cannot
 * be worked in (closed) or the project switch was refused.
 */
export async function workIn(c: Change | undefined): Promise<boolean> {
  if (!c?.id) return false;
  if (!workable(c)) {
    notify(`The change “${c.title || c.id}” is ${c.status}: it can no longer be worked in.`, 'info');
    return false;
  }
  const w = asWorking(c);
  if (w.projectId !== project.current && !(await selectProject(w.projectId))) return false;
  working.change = w;
  remember(w.projectId, w.id);
  return true;
}

/** Makes the change of that id the working change (read first). */
export async function workInId(id: string): Promise<boolean> {
  try {
    return await workIn((await graph.getChange(id)).change);
  } catch (e) {
    notify(errorMessage(e), 'error');
    return false;
  }
}

/** Leaves the working change: the person works in the project, in no change. */
export function stopWorking(): void {
  const w = working.change;
  working.change = undefined;
  if (w) remember(w.projectId, '');
}

/** Keeps the working change up to date with what was read of it (its title, its status); a closed change is left. */
export function refreshWorking(c: Change | undefined): void {
  const w = working.change;
  if (!c?.id || !w || w.id !== c.id) return;
  if (!workable(c)) return stopWorking();
  working.change = asWorking(c);
}

let restoring = 0;

/** Takes back the working change last selected in a project (none outside any project, or when it closed since). */
async function restore(projectKey: string, id: string): Promise<void> {
  const call = ++restoring;
  if (working.change && working.change.projectId === projectKey && (!id || working.change.id === id)) return;
  if (!projectKey || !id) {
    working.change = undefined;
    return;
  }
  try {
    const c = (await graph.getChange(id)).change;
    if (call !== restoring) return;
    if (c && workable(c) && (c.projectId || rootProject()) === projectKey) working.change = asWorking(c);
    else {
      working.change = undefined;
      remember(projectKey, '');
    }
  } catch {
    if (call === restoring) working.change = undefined;
  }
}

// the working change belongs to the active project: a project switch (from the header, the assistant, another tab's
// token) leaves it and takes back the one of the new project, once the preferences are read
$effect.root(() => {
  $effect(() => {
    if (!prefs.loaded) return;
    const p = project.current;
    const id = prefs.values.workingChanges[p] ?? '';
    untrack(() => void restore(p, id));
  });
});
