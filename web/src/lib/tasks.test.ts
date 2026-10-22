import { describe, expect, it } from 'vitest';
import type { ChangeAsk } from './api';
import { acceptable, byChange, isMine, myTasks, standing, taskKey, tasksOf } from './tasks';

const ask = (id: string, changeId: string, o: Partial<ChangeAsk> = {}): ChangeAsk => ({ id, changeId, changeTitle: `T ${changeId}`, projectId: 'P1', since: '2026-10-01T00:00:00Z', ...o });

describe('tasks', () => {
  const owned = ask('a', 'C1', { owner: 'ann', mayAnswer: true, since: '2026-10-03T00:00:00Z' });
  const role = ask('b', 'C2', { roles: ['dev'], mayAnswer: true, since: '2026-10-02T00:00:00Z' });
  const theirs = ask('c', 'C2', { owner: 'bob' });
  const other = ask('d', 'C3', { projectId: 'P2' });

  it('counts a task addressed to the person alone as accepted', () => {
    expect(isMine(owned, 'ann', new Set())).toBe(true);
    expect(isMine(theirs, 'ann', new Set())).toBe(false);
    expect(isMine(role, 'ann', new Set())).toBe(false);
    expect(isMine(role, 'ann', new Set([taskKey(role)]))).toBe(true);
    // accepted, but no longer theirs to answer
    expect(isMine({ ...role, mayAnswer: false }, 'ann', new Set([taskKey(role)]))).toBe(false);
  });

  it('offers to accept what the person may answer and does not own', () => {
    expect(acceptable(role, 'ann', new Set())).toBe(true);
    expect(acceptable(owned, 'ann', new Set())).toBe(false);
    expect(acceptable(theirs, 'ann', new Set())).toBe(false);
  });

  it('lists the person’s tasks across changes, the oldest first', () => {
    expect(myTasks([owned, role, theirs], 'ann', new Set([taskKey(role)])).map((a) => a.id)).toEqual(['b', 'a']);
  });

  it('lists the tasks of a change and of a project by change', () => {
    expect(tasksOf([owned, role, theirs], 'C2').map((a) => a.id)).toEqual(['c', 'b']);
    const g = byChange([owned, role, theirs, other], 'P1');
    expect(g.map((x) => x.changeId)).toEqual(['C2', 'C1']);
    expect(g[0].asks.map((a) => a.id)).toEqual(['c', 'b']);
    expect(byChange([other]).map((x) => x.title)).toEqual(['T C3']);
  });

  it('forgets the accepted tasks that no longer stand', () => {
    expect(standing([taskKey(role), 'C9|x'], [owned, role])).toEqual([taskKey(role)]);
  });
});
