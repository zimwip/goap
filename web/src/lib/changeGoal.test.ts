import { describe, expect, it } from 'vitest';
import { goalInfo } from './changeGoal';

describe('the goal of a change', () => {
  const m = { name: 'sdlc', goals: [{ name: 'deliver', description: 'Deliver end-to-end' }], processes: [{ name: 'release', description: 'Release it' }] };
  it('takes its description from a goal or a process of the methodology', () => {
    expect(goalInfo(m, 'deliver')).toEqual({ state: 'known', description: 'Deliver end-to-end' });
    expect(goalInfo(m, 'release')).toEqual({ state: 'known', description: 'Release it' });
  });
  it('shows a goal the methodology no longer declares as unknown, and waits for an unread methodology', () => {
    expect(goalInfo(m, 'gone').state).toBe('unknown');
    expect(goalInfo(undefined, 'deliver').state).toBe('loading');
  });
});
