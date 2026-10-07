import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const openTab = vi.hoisted(() => vi.fn());
const refreshChanges = vi.hoisted(() => vi.fn(async () => {}));
const selectProject = vi.hoisted(() => vi.fn(async () => {}));
const followChangeProject = vi.hoisted(() => vi.fn(async () => {}));
const notify = vi.hoisted(() => vi.fn());
const cat = vi.hoisted(() => ({ changes: { items: [] as { id: string; projectId?: string }[] } }));
vi.mock('../shell/tabs.svelte', async (orig) => ({ ...(await orig<object>()), openTab }));
vi.mock('../shell/workbench.svelte', () => ({ notify }));
vi.mock('../stores/catalog.svelte', () => ({ changes: cat.changes, refreshChanges }));
vi.mock('../stores/project.svelte', () => ({ selectProject, followChangeProject, applicable: { names: undefined } }));
const choices = vi.hoisted(() => ({ aliases: [] as { alias: string; provider?: string; model?: string }[], loaded: true }));
vi.mock('../stores/modelChoices.svelte', () => ({
  modelChoices: choices,
  refreshModelChoices: vi.fn(),
  aliasFlags: {
    get assistantEnabled() {
      return choices.aliases.some((a) => a.alias === 'assistant' && a.provider && a.model);
    },
  },
}));

import { actionLabel, runActions } from './actions';
import { assistantContext, clipBytes } from './context';
import { assistantEnabled } from './enabled';
import { COMMANDS } from '../shell/commands';

beforeEach(() => {
  openTab.mockReset();
  selectProject.mockReset();
  followChangeProject.mockReset();
  refreshChanges.mockReset();
  notify.mockReset();
  cat.changes.items = [];
});
afterEach(() => vi.unstubAllGlobals());

describe('assistant actions', () => {
  it('switches project, then opens the change after refreshing the catalog', async () => {
    await runActions([
      { type: 'select_project', args: { project: 'PROJ-B' } },
      { type: 'create_change', args: { title: 'T', intent: 'i' }, result: { changeId: 'CHG-9', project: 'PROJ-B' } },
    ]);
    expect(selectProject).toHaveBeenCalledWith('PROJ-B');
    expect(refreshChanges).toHaveBeenCalled();
    expect(openTab).toHaveBeenCalledWith({ kind: 'change', params: { id: 'CHG-9' } }, { pin: true });
  });

  it('opens a known change without refreshing and ignores unknown types', async () => {
    cat.changes.items = [{ id: 'CHG-1', projectId: 'PROJ-A' }];
    await runActions([{ type: 'open_change', args: { changeId: 'CHG-1' } }, { type: 'mystery' }]);
    expect(refreshChanges).not.toHaveBeenCalled();
    expect(followChangeProject).toHaveBeenCalledWith({ id: 'CHG-1', projectId: 'PROJ-A' });
    expect(openTab).toHaveBeenCalledTimes(1);
    expect(actionLabel({ type: 'mystery' })).toBe('');
  });

  it('labels the chips', () => {
    expect(actionLabel({ type: 'select_project', args: { project: 'P' } })).toBe('Switched to project P');
    expect(actionLabel({ type: 'open_change', args: { changeId: 'CHG-1' } })).toBe('Opened change CHG-1');
    expect(actionLabel({ type: 'create_change', args: { title: 'T' }, result: { changeId: 'CHG-2', title: 'Fix' } })).toBe('Created change Fix');
  });

  it('notifies a failure instead of throwing', async () => {
    selectProject.mockRejectedValueOnce(new Error('denied'));
    await runActions([{ type: 'select_project', args: { project: 'X' } }]);
    expect(notify).toHaveBeenCalledWith('denied', 'error');
  });
});

describe('assistant context', () => {
  const tab = { id: 'change:C1', kind: 'change', params: { id: 'C1' }, pinned: true };

  it('has the tab, subject, selection and project, and nothing else', () => {
    const c = assistantContext(tab, 'PROJ-A', 'chosen text');
    expect(c).toEqual({ tab: { kind: 'change', params: { id: 'C1' } }, subject: 'C1', selection: 'chosen text', project: 'PROJ-A' });
  });

  it('leaves out what is empty', () => {
    expect(assistantContext(undefined, '', '')).toEqual({});
  });

  it('caps the selection in bytes', () => {
    const c = assistantContext(tab, '', 'é'.repeat(3000));
    expect(new TextEncoder().encode(c.selection ?? '').length).toBeLessThanOrEqual(2000);
    expect(clipBytes('abc', 2)).toBe('ab');
  });
});

describe('assistant availability', () => {
  it('follows the assistant alias for the entry points', () => {
    vi.stubGlobal('window', { getSelection: () => undefined });
    choices.aliases = [];
    expect(assistantEnabled()).toBe(false);
    const ask = COMMANDS.filter((c) => c.id === 'assistant' || c.id === 'askAssistant');
    expect(ask).toHaveLength(2);
    expect(ask.every((c) => c.enabled?.() === false)).toBe(true);
    choices.aliases = [{ alias: 'assistant', provider: 'p', model: 'm' }];
    expect(assistantEnabled()).toBe(true);
    expect(ask.every((c) => c.enabled?.() === true)).toBe(true);
  });
});
