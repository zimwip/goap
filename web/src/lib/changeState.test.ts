import { describe, expect, it } from 'vitest';
import { blockers, needText, needsOf, ordered, runsText } from './changeState';

describe('change state', () => {
  it('reads a need by its meaning', () => {
    expect(needText({ condition: 'description_accepted', want: true, description: 'the description is rated acceptable' })).toBe('the description is rated acceptable');
    expect(needText({ condition: 'open_risks', want: false })).toBe('not: open risks');
  });
  it('falls back on the missing names', () => {
    expect(needsOf({ missing: ['a', '!b'] })).toEqual([
      { condition: 'a', want: true },
      { condition: 'b', want: false },
    ]);
  });
  it('lists the steps that cannot start, in the tree', () => {
    const steps = [
      { name: 'x', state: 'done' },
      { name: 'g', state: 'todo', steps: [{ name: 'y', state: 'todo', missing: ['c'] }, { name: 'z', state: 'ready' }] },
      { name: 'w', state: 'waiting', waiting: 'input', missing: ['d'] },
    ];
    expect(blockers(steps).map((s) => s.name)).toEqual(['y']);
  });
  it('counts runs and orders processes', () => {
    expect(runsText({ runs: 0 })).toBe('not run yet');
    expect(runsText({ runs: 3 })).toBe('3 runs');
    expect(ordered([{ process: 'a', state: 'done' }, { process: 'b', state: 'waiting' }, { process: 'c', state: 'todo' }]).map((p) => p.process)).toEqual(['b', 'c', 'a']);
  });
});
