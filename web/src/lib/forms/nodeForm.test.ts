import { describe, expect, it } from 'vitest';
import type { FormDef } from './types';
import { SELF_KEY, nodeEdit, nodeFormValues, nodeWrite } from './nodeForm';

const form: FormDef = {
  fields: [
    { name: 'title', widget: 'text', required: true },
    { name: 'priority', widget: 'select', constraints: { options: ['low', 'high'] } },
    { name: 'note', widget: 'text', visibleWhen: [{ field: 'priority', equals: 'high' }] },
    { name: 'satisfies', widget: 'link', constraints: { fromKey: SELF_KEY, linkTypes: ['alm@satisfies'] } },
    { name: 'other', widget: 'link', constraints: { fromKey: SELF_KEY } },
  ],
};
const links = [
  { id: 'L1', type: 'alm@satisfies', to: 'N1' },
  { id: 'L2', type: 'alm@refines', to: 'R1' },
];

describe('the form of a node type on an existing node', () => {
  it('starts from the properties and gives each link field its links', () => {
    const v = nodeFormValues(form, { title: 'T', priority: 'low', extra: 1 }, links);
    expect(v.title).toBe('T');
    expect(v.extra).toBeUndefined();
    expect(v.satisfies).toEqual([{ from: SELF_KEY, type: 'alm@satisfies', to: 'N1' }]);
    expect(v.other).toEqual([{ from: SELF_KEY, type: 'alm@refines', to: 'R1' }]);
  });

  it('changes only what differs, adds and removes links', () => {
    const props = { title: 'T', priority: 'low', note: 'kept' };
    const v = nodeFormValues(form, props, links);
    v.title = 'T2';
    v.satisfies = [{ from: SELF_KEY, type: 'alm@satisfies', to: 'N2' }];
    const e = nodeEdit(form, v, props, links);
    expect(e.props).toEqual({ title: 'T2' }); // note is hidden (priority low): kept
    expect(e.add).toEqual([{ type: 'alm@satisfies', to: 'N2' }]);
    expect(e.remove).toEqual(['L1']);
  });

  it('clears a shown string left blank', () => {
    const props = { title: 'T', priority: 'high', note: 'n' };
    const v = { ...nodeFormValues(form, props, links), note: '' };
    expect(nodeEdit(form, v, props, links).props).toEqual({ note: '' });
  });

  it('a creation keeps its links from the node', () => {
    const w = nodeWrite(form, { title: 'T', satisfies: [{ from: SELF_KEY, type: 'alm@satisfies', to: 'N1' }] });
    expect(w).toEqual({ props: { title: 'T' }, links: [{ type: 'alm@satisfies', to: 'N1' }] });
  });
});
