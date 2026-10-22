import { describe, expect, it } from 'vitest';
import type { ChangeAsk } from './api';
import { availableTasks, byChange, heldByOther, holderLabel, isAvailable, isMine, myTasks, taskKey, tasksOf } from './tasks';

const ask = (id: string, changeId: string, o: Partial<ChangeAsk> = {}): ChangeAsk => ({ id, changeId, changeTitle: `T ${changeId}`, projectId: 'P1', since: '2026-10-01T00:00:00Z', ...o });

describe('tasks', () => {
  const owned = ask('a', 'C1', { owner: 'ann', mayAnswer: true, since: '2026-10-03T00:00:00Z' });
  const role = ask('b', 'C2', { roles: ['dev'], mayAnswer: true, mayAccept: true, since: '2026-10-02T00:00:00Z' });
  const theirs = ask('c', 'C2', { owner: 'bob' });
  const other = ask('d', 'C3', { projectId: 'P2' });

  it('counts a task held by the person, or addressed to them alone, as theirs', () => {
    expect(isMine(owned, 'ann')).toBe(true);
    expect(isMine(theirs, 'ann')).toBe(false);
    expect(isMine(role, 'ann')).toBe(false);
    expect(isMine({ ...role, assignee: 'ann' }, 'ann')).toBe(true);
    // given to someone else, it is no longer theirs even when addressed to them
    expect(isMine({ ...owned, assignee: 'bob' }, 'ann')).toBe(false);
    expect(isMine(owned, '')).toBe(false);
  });

  it('offers the tasks the person may answer and nobody holds', () => {
    expect(isAvailable(role, 'ann')).toBe(true);
    expect(isAvailable(owned, 'ann')).toBe(false);
    expect(isAvailable(theirs, 'ann')).toBe(false);
    expect(isAvailable({ ...role, assignee: 'bob' }, 'ann')).toBe(false);
    expect(isAvailable({ ...role, declined: true }, 'ann')).toBe(false);
    expect(heldByOther({ ...role, assignee: 'bob' }, 'ann')).toBe(true);
    expect(holderLabel({ ...role, assignee: 'ann' }, 'ann')).toBe('you');
    expect(holderLabel(role, 'ann')).toBe('');
  });

  it('lists the person’s tasks and the available ones across changes, the oldest first', () => {
    const held = ask('e', 'C4', { roles: ['dev'], assignee: 'ann', since: '2026-10-04T00:00:00Z' });
    expect(myTasks([owned, role, theirs, held], 'ann').map((a) => a.id)).toEqual(['a', 'e']);
    const older = ask('f', 'C4', { mayAnswer: true, since: '2026-09-30T00:00:00Z' });
    expect(availableTasks([owned, role, theirs, older], 'ann').map((a) => a.id)).toEqual(['f', 'b']);
  });

  it('lists the tasks of a change and of a project by change', () => {
    expect(tasksOf([owned, role, theirs], 'C2').map((a) => a.id)).toEqual(['c', 'b']);
    const g = byChange([owned, role, theirs, other], 'P1');
    expect(g.map((x) => x.changeId)).toEqual(['C2', 'C1']);
    expect(g[0].asks.map((a) => a.id)).toEqual(['c', 'b']);
    expect(byChange([other]).map((x) => x.title)).toEqual(['T C3']);
    expect(taskKey(role)).toBe('C2|b');
  });
});
