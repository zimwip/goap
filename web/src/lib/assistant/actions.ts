// The UI actions an assistant message carries (ADR 0087), run by the web: the server has already checked them.
import type { ConversationAction } from '../api';
import { openTab } from '../shell/tabs.svelte';
import { notify } from '../shell/workbench.svelte';
import { changes, refreshChanges } from '../stores/catalog.svelte';
import { followChangeProject, selectProject } from '../stores/project.svelte';

const str = (o: unknown, k: string): string => {
  const v = (o as Record<string, unknown> | undefined)?.[k];
  return typeof v === 'string' ? v : '';
};

/** The change an open_change / create_change action is about. */
export function actionChangeId(a: ConversationAction): string {
  return str(a.result, 'changeId') || str(a.args, 'changeId');
}

/** A short label for the chip of an action; '' for a type this web does not know. */
export function actionLabel(a: ConversationAction): string {
  switch (a.type) {
    case 'select_project':
      return `Switched to project ${str(a.args, 'project')}`;
    case 'open_change':
      return `Opened change ${str(a.result, 'title') || actionChangeId(a)}`;
    case 'create_change':
      return `Created change ${str(a.result, 'title') || str(a.args, 'title') || actionChangeId(a)}`;
    default:
      return '';
  }
}

/** Runs one action; a failure is notified, never thrown. */
export async function runAction(a: ConversationAction): Promise<void> {
  try {
    switch (a.type) {
      case 'select_project': {
        const p = str(a.args, 'project');
        if (p) await selectProject(p);
        break;
      }
      case 'open_change':
      case 'create_change': {
        const id = actionChangeId(a);
        if (!id) break;
        // the catalog lists the change a person has just been given
        if (a.type === 'create_change' || !changes.items.some((c) => c.id === id)) await refreshChanges();
        const known = changes.items.find((c) => c.id === id);
        await followChangeProject({ id, projectId: known?.projectId || str(a.result, 'project') });
        openTab({ kind: 'change', params: { id } }, { pin: true });
        break;
      }
    }
  } catch (e) {
    notify(e instanceof Error ? e.message : String(e), 'error');
  }
}

/** Runs the actions of a message one after the other (a project switch before the change it is for). */
export async function runActions(actions: ConversationAction[] | undefined): Promise<void> {
  for (const a of actions ?? []) await runAction(a);
}
