// The UI actions an assistant message carries (ADR 0087), run by the web: the server has already checked them.
import type { ConversationAction, ConversationMessage, UiToolAction } from '../api';
import { runTool } from '../assist/registry.svelte';
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

/** The screen-tool action an action is, else undefined. */
export const asUiTool = (a: ConversationAction): UiToolAction | undefined => (a.type === 'ui_tool' ? (a as UiToolAction) : undefined);

/** A short label for the chip of an action; '' for a type this web does not know. */
export function actionLabel(a: ConversationAction): string {
  switch (a.type) {
    case 'select_project':
      return `Switched to project ${str(a.args, 'project')}`;
    case 'open_change':
      return `Opened change ${str(a.result, 'title') || actionChangeId(a)}`;
    case 'create_change':
      return `Created change ${str(a.result, 'title') || str(a.args, 'title') || actionChangeId(a)}`;
    case 'ui_tool':
      return (a as UiToolAction).label || `Screen action ${(a as UiToolAction).tool ?? ''}`.trim();
    default:
      return '';
  }
}

/**
 * The label of any action, for the chip: the known ones, else the label the server gave it, else its type (a server
 * tool this web does not know yet still shows what happened).
 */
export function actionChipLabel(a: ConversationAction): string {
  return actionLabel(a) || str(a, 'label') || str(a.args, 'label') || a.type.replace(/[_.-]+/g, ' ');
}

/** Can the chip repeat the action (a click runs it again)? */
export const repeatable = (a: ConversationAction): boolean => ['select_project', 'open_change', 'create_change'].includes(a.type) || (a.type === 'ui_tool' && (a as UiToolAction).level === 'effect');

/** Runs the screen tool of an action through the registry: the tool must be registered now. */
export async function runUiTool(a: UiToolAction): Promise<{ ok: true } | { ok: false; error: string }> {
  const r = await runTool(a.tool, (a.args ?? {}) as Record<string, unknown>);
  return r;
}

/** Runs one action; a failure is notified, never thrown. */
export async function runAction(a: ConversationAction): Promise<void> {
  try {
    switch (a.type) {
      case 'ui_tool': {
        // a repeated click on an effect chip; a write is decided on its card, never run from here
        const u = a as UiToolAction;
        if (u.level !== 'effect') break;
        const r = await runUiTool(u);
        if (!r.ok) notify(r.error, 'error');
        break;
      }
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

/** Reports the outcome of a screen tool of a message (best effort: the caller ignores a failure). */
export type Reporter = (index: number, status: 'done' | 'failed', error?: string) => Promise<void>;

/**
 * Runs the actions of a message that turned done, one after the other (a project switch before the change it is for).
 * An effect screen tool runs at once and its outcome is reported; a write proposal and a start_agent proposal run
 * nothing here: they wait for the person on their card.
 */
export async function runActions(m: Pick<ConversationMessage, 'actions'>, report?: Reporter): Promise<void> {
  const actions = m.actions ?? [];
  for (const [i, a] of actions.entries()) {
    const u = asUiTool(a);
    if (u) {
      if (u.level !== 'effect' || u.status !== 'requested') continue;
      const r = await runUiTool(u);
      if (!r.ok) notify(r.error, 'error');
      try {
        await report?.(i, r.ok ? 'done' : 'failed', r.ok ? undefined : r.error);
      } catch {
        /* the outcome is optional for an effect */
      }
      continue;
    }
    await runAction(a);
  }
}
