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

import { actionChipLabel, actionLabel, repeatable, runActions } from './actions';
import { registerAssist, resetRegistry } from '../assist/registry.svelte';
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
    await runActions({
      actions: [
        { type: 'select_project', args: { project: 'PROJ-B' } },
        { type: 'create_change', args: { title: 'T', intent: 'i' }, result: { changeId: 'CHG-9', project: 'PROJ-B' } },
      ],
    });
    expect(selectProject).toHaveBeenCalledWith('PROJ-B');
    expect(refreshChanges).toHaveBeenCalled();
    expect(openTab).toHaveBeenCalledWith({ kind: 'change', params: { id: 'CHG-9' } }, { pin: true });
  });

  it('opens a known change without refreshing and ignores unknown types', async () => {
    cat.changes.items = [{ id: 'CHG-1', projectId: 'PROJ-A' }];
    await runActions({ actions: [{ type: 'open_change', args: { changeId: 'CHG-1' } }, { type: 'mystery' }] });
    expect(refreshChanges).not.toHaveBeenCalled();
    expect(followChangeProject).toHaveBeenCalledWith({ id: 'CHG-1', projectId: 'PROJ-A' });
    expect(openTab).toHaveBeenCalledTimes(1);
    expect(actionLabel({ type: 'mystery' })).toBe('');
    // the chip of a type this web does not know still says what happened
    expect(actionChipLabel({ type: 'methodology_query' })).toBe('methodology query');
    expect(actionChipLabel({ type: 'x', args: { label: 'Asked the catalog' } })).toBe('Asked the catalog');
  });

  it('labels the chips', () => {
    expect(actionLabel({ type: 'select_project', args: { project: 'P' } })).toBe('Switched to project P');
    expect(actionLabel({ type: 'open_change', args: { changeId: 'CHG-1' } })).toBe('Opened change CHG-1');
    expect(actionLabel({ type: 'create_change', args: { title: 'T' }, result: { changeId: 'CHG-2', title: 'Fix' } })).toBe('Created change Fix');
  });

  it('notifies a failure instead of throwing', async () => {
    selectProject.mockRejectedValueOnce(new Error('denied'));
    await runActions({ actions: [{ type: 'select_project', args: { project: 'X' } }] });
    expect(notify).toHaveBeenCalledWith('denied', 'error');
  });
});

describe('screen tool executor', () => {
  const effect = (over: Record<string, unknown> = {}) => ({ type: 'ui_tool', status: 'requested', level: 'effect', tool: 'filter_impacts', args: { review: 'proposed' }, label: 'Show proposed', ...over });
  const write = (over: Record<string, unknown> = {}) => ({ type: 'ui_tool', status: 'proposed', level: 'write', tool: 'rename_change', args: { title: 'New' }, label: 'Rename', project: 'P', ...over });
  beforeEach(() => resetRegistry());

  it('runs an effect through the registry and reports done', async () => {
    const run = vi.fn();
    const off = registerAssist({ tools: [{ name: 'filter_impacts', run }] });
    const report = vi.fn(async () => {});
    await runActions({ actions: [effect()] }, report);
    expect(run).toHaveBeenCalledWith({ review: 'proposed' });
    expect(report).toHaveBeenCalledWith(0, 'done', undefined);
    off();
  });

  it('reports failed with a clear error when the tool is not on the screen any more', async () => {
    const report = vi.fn(async () => {});
    await runActions({ actions: [effect()] }, report);
    expect(report).toHaveBeenCalledWith(0, 'failed', expect.stringContaining('no longer offers'));
    expect(notify).toHaveBeenCalledWith(expect.stringContaining('no longer offers'), 'error');
  });

  it('reports a failure of the tool itself, and ignores a refused report', async () => {
    const off = registerAssist({ tools: [{ name: 'filter_impacts', run: () => 'nope' }] });
    const report = vi.fn(async () => {
      throw new Error('already reported');
    });
    await runActions({ actions: [effect()] }, report);
    expect(report).toHaveBeenCalledWith(0, 'failed', 'nope');
    off();
  });

  it('never runs a write proposal, nor an effect that is not "requested"', async () => {
    const run = vi.fn();
    const off = registerAssist({ tools: [{ name: 'rename_change', run }, { name: 'filter_impacts', run }] });
    const report = vi.fn(async () => {});
    await runActions({ actions: [write(), write({ status: 'accepted' }), effect({ status: 'done' }), { type: 'start_agent', args: {}, status: 'proposed' } as never] }, report);
    expect(run).not.toHaveBeenCalled();
    expect(report).not.toHaveBeenCalled();
    off();
  });

  it('a click on an effect chip runs it again, a write chip never does', async () => {
    const run = vi.fn();
    const off = registerAssist({ tools: [{ name: 'filter_impacts', run }, { name: 'rename_change', run }] });
    expect(repeatable(effect())).toBe(true);
    expect(repeatable(write())).toBe(false);
    const { runAction } = await import('./actions');
    await runAction(write());
    expect(run).not.toHaveBeenCalled();
    await runAction(effect());
    expect(run).toHaveBeenCalledTimes(1);
    off();
  });

  it('labels a screen action by its label, else its tool', () => {
    expect(actionLabel(effect())).toBe('Show proposed');
    expect(actionLabel(effect({ label: '' }))).toBe('Screen action filter_impacts');
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
