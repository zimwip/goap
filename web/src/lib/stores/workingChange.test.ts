import { beforeEach, describe, expect, it, vi } from 'vitest';

const proj = vi.hoisted(() => ({ project: { current: 'PROJ-A' }, selectProject: vi.fn(async (k: string) => ((proj.project.current = k), true)) }));
const prefs = vi.hoisted(() => ({ prefs: { loaded: false, values: { workingChanges: {} as Record<string, string> } }, editPrefs: vi.fn(async (p: { workingChanges: Record<string, string> }) => void (prefs.prefs.values.workingChanges = p.workingChanges)) }));
const notify = vi.hoisted(() => vi.fn());
vi.mock('./project.svelte', () => proj);
vi.mock('./preferences.svelte', () => prefs);
vi.mock('./session.svelte', () => ({ rootProject: () => 'PROJ-ROOT' }));
vi.mock('../shell/workbench.svelte', () => ({ notify }));
vi.mock('../api', () => ({ graph: { getChange: vi.fn() }, errorMessage: String }));

import { isWorking, refreshWorking, stopWorking, workIn, working } from './workingChange.svelte';

beforeEach(() => {
  proj.project.current = 'PROJ-A';
  proj.selectProject.mockClear();
  prefs.prefs.values.workingChanges = {};
  notify.mockReset();
  working.change = undefined;
});

describe('working change', () => {
  it('works in a change of the active project and remembers it for that project', async () => {
    expect(await workIn({ id: 'C1', title: 'One', projectId: 'PROJ-A', status: 'active', namespace: 'alm' })).toBe(true);
    expect(proj.selectProject).not.toHaveBeenCalled();
    expect(isWorking('C1')).toBe(true);
    expect(prefs.prefs.values.workingChanges).toEqual({ 'PROJ-A': 'C1' });
    stopWorking();
    expect(working.change).toBeUndefined();
    expect(prefs.prefs.values.workingChanges).toEqual({});
  });

  it('switches to the project of the change first, the root project for a change naming none', async () => {
    expect(await workIn({ id: 'C2', projectId: 'PROJ-B', status: 'draft' })).toBe(true);
    expect(proj.selectProject).toHaveBeenCalledWith('PROJ-B');
    expect(await workIn({ id: 'C3', status: 'draft' })).toBe(true);
    expect(proj.selectProject).toHaveBeenLastCalledWith('PROJ-ROOT');
    expect(working.change?.projectId).toBe('PROJ-ROOT');
  });

  it('refuses a closed change and leaves one that closes', async () => {
    expect(await workIn({ id: 'C4', projectId: 'PROJ-A', status: 'applied' })).toBe(false);
    expect(notify).toHaveBeenCalled();
    await workIn({ id: 'C5', projectId: 'PROJ-A', status: 'active', title: 'Five' });
    refreshWorking({ id: 'C5', projectId: 'PROJ-A', status: 'active', title: 'Renamed' });
    expect(working.change?.title).toBe('Renamed');
    refreshWorking({ id: 'C5', projectId: 'PROJ-A', status: 'abandoned' });
    expect(working.change).toBeUndefined();
  });

  it('keeps the change when the project switch is refused', async () => {
    proj.selectProject.mockImplementationOnce(async () => false);
    expect(await workIn({ id: 'C6', projectId: 'PROJ-SECRET', status: 'active' })).toBe(false);
    expect(working.change).toBeUndefined();
  });
});
