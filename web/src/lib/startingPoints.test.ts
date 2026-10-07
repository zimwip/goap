import { describe, expect, it } from 'vitest';
import type { StartingPoint, StartingPointsResponse } from './api';
import { confirmText, emptyText, kindText, pointById, rolesText, showStartingPoints, startable, startableIds, startRequest, waitingLine, whyText } from './startingPoints';

const point = (over: Partial<StartingPoint> = {}): StartingPoint => ({
  id: 'p/s',
  kind: 'step',
  process: 'p',
  name: 's',
  launch: { methodology: 'm', agent: 'p', goal: 'p/s', changeId: 'C1' },
  mayRun: true,
  ...over,
});

describe('starting points', () => {
  it('is shown for an open change that has a methodology and a goal', () => {
    expect(showStartingPoints({ status: 'active', methodology: 'm', goal: 'g' })).toBe(true);
    expect(showStartingPoints({ status: 'draft', methodology: 'm', goal: 'g' })).toBe(true);
    for (const status of ['applied', 'abandoned', 'committed']) expect(showStartingPoints({ status, methodology: 'm', goal: 'g' })).toBe(false);
    // no goal or no methodology: the panel is still there (the server falls back, or a methodology is chosen)
    expect(showStartingPoints({ status: 'active', methodology: 'm' })).toBe(true);
    expect(showStartingPoints({ status: 'active' })).toBe(true);
    expect(showStartingPoints(undefined)).toBe(false);
  });

  it('starts a point with the launch the engine gave it, and names no action', () => {
    const r = startRequest(point());
    expect(r).toMatchObject({ methodology: 'm', agent: 'p', goal: 'p/s', changeId: 'C1' });
    expect(Object.keys(r).sort()).toEqual(['agent', 'changeId', 'goal', 'intent', 'methodology']);
  });

  it('offers only what the caller may start and nothing carries out', () => {
    const r: StartingPointsResponse = { points: [point(), point({ id: 'p/r', running: true }), point({ id: 'p/n', mayRun: false })] };
    expect(startableIds(r)).toEqual(['p/s']);
    expect(startable(point({ mayRun: false }))).toBe(false);
    expect(pointById(r, 'p/r')?.running).toBe(true);
    expect(pointById(r, 'x')).toBeUndefined();
    expect(startableIds(undefined)).toEqual([]);
  });

  it('says why a step is possible, and who can do it', () => {
    expect(whyText(point({ why: ['framed', '!blocked'] }))).toBe('framed, !blocked');
    expect(whyText(point())).toBe('no precondition');
    expect(rolesText(point({ mayRun: false, needRoles: ['developer'] }))).toBe('needs the role developer');
    expect(rolesText(point({ responsible: 'analyst' }))).toBe('role analyst');
    expect(rolesText(point())).toBe('any member');
    expect(kindText(point())).toBe('step');
    expect(kindText(point({ kind: 'method', method: 'peer', capability: 'verification' }))).toBe('method peer (verification)');
    expect(kindText(point({ kind: 'method', method: 'peer', parent: 'p/verify' }))).toBe('step of the method peer');
  });

  it('counts the steps that wait without proposing them', () => {
    expect(waitingLine(undefined)).toBe('');
    expect(waitingLine({ blockedCount: 1, blocked: [{ id: 'a', name: 'a', missing: ['x'] }] })).toBe('1 more step waits for: x');
    expect(waitingLine({ blockedCount: 7, blocked: [{ id: 'a', name: 'a', missing: ['x', 'y'] }, { id: 'b', name: 'b', missing: ['y', 'z'] }] })).toBe('7 more steps wait for: x, y, z');
  });

  it('tells what will start before it does', () => {
    expect(confirmText(point({ produces: ['framed'] }), 'deliver')).toContain('the goal deliver');
    expect(confirmText(point({ produces: ['framed'] }), 'deliver')).toContain('framed');
    expect(confirmText(point({ kind: 'method', method: 'peer', name: 'verify' }), '')).toContain('the method peer for “verify”');
    expect(emptyText({ reason: 'the goal is reached' })).toBe('the goal is reached');
    expect(emptyText(undefined)).toBe('No step is possible now.');
  });
});
