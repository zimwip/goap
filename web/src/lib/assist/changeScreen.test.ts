import { describe, expect, it } from 'vitest';
import { changeSummary, diffSummary, impactEntities, stepEntities, transitionEntities } from './changeScreen';

describe('change screen facts', () => {
  const impacts = [
    { id: 'I1', key: 'REQ-1', type: 'alm@Requirement', intent: 'modified', review: 'accepted', state: 'approved', committable: true },
    { id: 'I2', key: 'REQ-2', type: 'alm@Requirement', intent: 'created', review: 'proposed', state: 'draft', committable: false },
    { id: 'I3', key: 'TC-1', review: undefined },
  ];

  it('lists the impacts awaiting review first, with their state and a few props', () => {
    const e = impactEntities(impacts);
    expect(e.map((x) => x.id)).toEqual(['I2', 'I3', 'I1']);
    expect(e[0]).toEqual({ type: 'impact', id: 'I2', label: 'REQ-2', state: 'proposed', props: { type: 'alm@Requirement', intent: 'created', nodeState: 'draft', committable: 'no' } });
    expect(e[1].state).toBe('proposed');
  });

  it('summarises the change in one sentence', () => {
    const s = changeSummary({ title: 'Fix', status: 'active', lifecycle: 'change', state: 'draft', project: 'PROJ-A', methodology: 'sdlc' }, impacts, { pane: 'impacts', scope: 'opt-1', filter: 'proposed' });
    expect(s).toBe('Change “Fix”, active; lifecycle change in state draft; project PROJ-A; methodology sdlc; 3 impacts, 2 awaiting review; pane impacts; scope opt-1; filtered to proposed.');
    expect(changeSummary({ title: 'T' }, [], {})).toBe('Change “T”, unknown; 0 impacts, 0 awaiting review.');
  });

  it('flags the impacts that modify nothing, so the model withdraws them rather than accept them', () => {
    expect(impactEntities([{ id: 'I9', key: 'REQ-9', review: 'proposed', modifies: false }])[0].props).toEqual({ modifies: 'no' });
    expect(impactEntities([{ id: 'I9', key: 'REQ-9', modifies: true }])[0].props).toBeUndefined();
  });

  it('tells what the change can commit and merge (ADR 0102)', () => {
    expect(changeSummary({ title: 'T', status: 'active', commit: { canCommit: true, canMerge: false, mergeWhy: 'Nothing committed on the branch of the change yet' } }, [])).toBe(
      'Change “T”, active; 0 impacts, 0 awaiting review; commit possible; merge not possible (Nothing committed on the branch of the change yet).',
    );
    expect(changeSummary({ title: 'T', status: 'active', commit: { committed: true, canMerge: true, mergeInto: 'parent change C1' } }, [])).toBe(
      'Change “T”, active; 0 impacts, 0 awaiting review; committed on its branch; nothing to commit; merge possible into parent change C1.',
    );
  });

  it('summarises a diff by property names, never values', () => {
    const d = diffSummary({ a: 1, b: 'x', c: 'gone' }, { a: 1, b: 'y', d: 'new' });
    expect(d).toBe('Edits: adds d; changes b; clears c.');
    expect(d).not.toContain('new"');
    expect(diffSummary({ a: 1 }, { a: 1 })).toBe('No edit of the properties.');
  });

  it('lists the transitions out of the state and the decided points, by name', () => {
    const e = transitionEntities([{ name: 'approve', to: 'approved', needsDecision: true }, { name: 'back', to: 'draft' }], [{ id: 'D1', label: 'Which? → A' }]);
    expect(e).toEqual([
      { type: 'transition', id: 'approve', label: 'approve', props: { to: 'approved', needsDecision: 'yes' } },
      { type: 'transition', id: 'back', label: 'back', props: { to: 'draft' } },
      { type: 'decision', id: 'D1', label: 'Which? → A', state: 'decided' },
    ]);
  });
});

describe('step entities', () => {
  it('lists the possible steps with their state and why', () => {
    const e = stepEntities([
      { id: 'p/a', name: 'a', kind: 'step', why: ['framed'], produces: ['analysed'], mayRun: true },
      { id: 'p/b', name: 'b', running: true, mayRun: true },
      { id: 'p/c', name: 'c' },
    ]);
    expect(e.map((x) => [x.type, x.id, x.state])).toEqual([['step', 'p/a', 'possible'], ['step', 'p/b', 'running'], ['step', 'p/c', 'not allowed']]);
    expect(e[0].props).toEqual({ kind: 'step', why: 'framed', produces: 'analysed' });
    expect(e[1].props).toBeUndefined();
  });
});
