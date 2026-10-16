import { describe, expect, it } from 'vitest';
import { blockers, composeProcess, factLines, needDetail, needText, needsOf, ordered, runsText, statesByPath } from './changeState';
import type { MethodologyView, ProcessState } from './api';

describe('change state', () => {
  it('reads a need by its meaning', () => {
    expect(needText({ condition: 'description_accepted', want: true, description: 'the description is rated acceptable' })).toBe('the description is rated acceptable');
    expect(needText({ condition: 'open_risks', want: false })).toBe('not: open risks');
  });
  it('says what is to be reached first', () => {
    const n = { condition: 'description_accepted', want: true, description: 'the description is accepted',
      requires: [{ condition: 'description_rated', want: true, description: 'the description has been rated', producedBy: ['rate_description'] }] };
    expect(needDetail(n)).toBe('the description is accepted, first: the description has been rated (by rate_description)');
    expect(needDetail({ condition: 'a', want: true })).toBe('a');
  });
  it('says which term of the condition is missing', () => {
    const n = { condition: 'description_accepted', want: true, description: 'the description is accepted',
      facts: [{ text: 'assessments.exists(a, ...)', over: 'assessments', holds: false, parts: [
        { text: 'a.key == "description"', holds: true }, { text: 'a.current', holds: true }, { text: 'a.score >= 3', holds: false, missing: true }] }] };
    expect(needDetail(n)).toBe('the description is accepted, missing: assessments: a.score >= 3');
    expect(factLines(n).map((f) => `${f.depth}${f.holds ? '+' : '-'}${f.text}`)).toEqual(['0-in assessments', '1+a.key == "description"', '1+a.current', '1-a.score >= 3']);
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

describe('the compiled view bound to the change (ADR 0126 step 4)', () => {
  const view: MethodologyView = {
    methodology: 'm',
    processes: [
      {
        name: 'delivery',
        kind: 'process',
        reactive: true,
        steps: [
          { path: 'delivery/write', name: 'write', owner: 'delivery', method: 'action', target: 'write', guidance: 'Write it.', entry: { accepted: true }, exit: { written: true } },
          {
            path: 'delivery/ship',
            name: 'ship',
            owner: 'delivery',
            method: 'steps',
            steps: [{ path: 'delivery/ship/tag', name: 'tag', owner: 'delivery', method: 'manual' }],
          },
        ],
      },
    ],
    conditions: {
      accepted: { description: 'the description is accepted', requires: ['rated'] },
      rated: { description: 'the description is rated', producedBy: ['rate'] },
    },
  };
  const state: ProcessState = {
    process: 'delivery',
    state: 'todo',
    steps: [
      { path: 'delivery/write', state: 'todo', missing: ['accepted'], needs: [{ condition: 'accepted', want: true, requires: ['rated'] }] },
      { path: 'delivery/ship', state: 'todo', steps: [{ path: 'delivery/ship/tag', state: 'ready' }] },
    ],
  };

  it('draws the steps of the view with the binding laid over them', () => {
    const { activity, steps } = composeProcess(view, state);
    expect(activity?.reactive).toBe(true);
    expect(steps.map((s) => [s.name, s.state, s.guidance])).toEqual([
      ['write', 'todo', 'Write it.'],
      ['ship', 'todo', undefined],
    ]);
    expect(steps[1].steps?.[0]).toMatchObject({ name: 'tag', state: 'ready' });
  });

  it('reads what a need means and what is to be reached first from the view', () => {
    const [n] = composeProcess(view, state).steps[0].needs ?? [];
    expect(n).toMatchObject({ condition: 'accepted', want: true, description: 'the description is accepted' });
    expect(needDetail(n)).toBe('the description is accepted, first: the description is rated (by rate)');
  });

  it('gives the flow the state of every step by path', () => {
    expect(statesByPath(composeProcess(view, state).steps)).toEqual({ 'delivery/write': 'todo', 'delivery/ship': 'todo', 'delivery/ship/tag': 'ready' });
  });

  it('draws nothing of a process the view does not know', () => {
    expect(composeProcess(undefined, state).steps).toEqual([]);
  });
});
