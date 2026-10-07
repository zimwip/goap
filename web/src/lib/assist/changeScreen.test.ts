import { describe, expect, it } from 'vitest';
import { changeSummary, diffSummary, impactEntities } from './changeScreen';

describe('change screen facts', () => {
  const impacts = [
    { id: 'I1', key: 'REQ-1', type: 'alm@Requirement', intent: 'modified', review: 'accepted', state: 'approved', landable: true },
    { id: 'I2', key: 'REQ-2', type: 'alm@Requirement', intent: 'created', review: 'proposed', state: 'draft', landable: false },
    { id: 'I3', key: 'TC-1', review: undefined },
  ];

  it('lists the impacts awaiting review first, with their state and a few props', () => {
    const e = impactEntities(impacts);
    expect(e.map((x) => x.id)).toEqual(['I2', 'I3', 'I1']);
    expect(e[0]).toEqual({ type: 'impact', id: 'I2', label: 'REQ-2', state: 'proposed', props: { type: 'alm@Requirement', intent: 'created', nodeState: 'draft', landable: 'no' } });
    expect(e[1].state).toBe('proposed');
  });

  it('summarises the change in one sentence', () => {
    const s = changeSummary({ title: 'Fix', status: 'active', lifecycle: 'change', state: 'draft', project: 'PROJ-A', methodology: 'sdlc' }, impacts, { pane: 'impacts', scope: 'opt-1', filter: 'proposed' });
    expect(s).toBe('Change “Fix”, active; lifecycle change in state draft; project PROJ-A; methodology sdlc; 3 impacts, 2 awaiting review; pane impacts; scope opt-1; filtered to proposed.');
    expect(changeSummary({ title: 'T' }, [], {})).toBe('Change “T”, unknown; 0 impacts, 0 awaiting review.');
  });

  it('summarises a diff by property names, never values', () => {
    const d = diffSummary({ a: 1, b: 'x', c: 'gone' }, { a: 1, b: 'y', d: 'new' });
    expect(d).toBe('Edits: adds d; changes b; clears c.');
    expect(d).not.toContain('new"');
    expect(diffSummary({ a: 1 }, { a: 1 })).toBe('No edit of the properties.');
  });
});
