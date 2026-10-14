import { describe, expect, it } from 'vitest';
import { nodeChanges } from './impactDiff';

describe('nodeChanges', () => {
  const base = {
    node: { id: 'n1', version: 2, key: 'REQ-1', state: 'draft', owner: 'u1', props: { title: 'Old', tags: ['a'], keep: { x: 1, y: 2 } } },
    out: [
      { id: 'l1', type: 'alm@satisfies', to: { id: 'n2', version: 1 } },
      { id: 'l2', type: 'alm@traces', to: { id: 'n3', version: 4 }, props: { weight: 1 } },
    ],
  };

  it('lists state, owner, properties and links in a stable order', () => {
    const after = {
      node: { id: 'n1', version: 0, key: 'REQ-1', state: 'approved', owner: 'u2', props: { title: 'New', keep: { y: 2, x: 1 }, extra: 3 } },
      out: [
        // the same link, now to a draft of its target: not a modification
        { id: 'd1', type: 'alm@satisfies', to: { id: 'n2', version: 0 } },
        { id: 'd2', type: 'alm@traces', to: { id: 'n3', version: 4 }, props: { weight: 2 } },
        { id: 'd3', type: 'alm@refines', to: { id: 'n4', version: 0 } },
      ],
    };
    const keys: Record<string, string> = { n3: 'FN-3', n4: 'FN-4' };
    expect(nodeChanges(base, after, (id) => keys[id] ?? id)).toEqual([
      { kind: 'state', op: 'changed', old: 'draft', new: 'approved' },
      { kind: 'owner', op: 'changed', old: 'u1', new: 'u2' },
      { kind: 'property', name: 'extra', op: 'added', new: 3 },
      { kind: 'property', name: 'tags', op: 'removed', old: ['a'] },
      { kind: 'property', name: 'title', op: 'changed', old: 'Old', new: 'New' },
      { kind: 'link', name: 'alm@refines', target: 'FN-4', op: 'added' },
      { kind: 'link', name: 'alm@traces', target: 'FN-3', op: 'changed', old: { weight: 1 }, new: { weight: 2 } },
    ]);
  });

  it('finds nothing in a draft equal to its base', () => {
    expect(nodeChanges(base, { node: { ...base.node, version: 0 }, out: base.out.map((l) => ({ ...l, id: `d-${l.id}` })) })).toEqual([]);
  });

  it('adds everything of a node the change creates, but its owner', () => {
    const changes = nodeChanges(undefined, { node: { id: 'n9', state: 'draft', owner: 'u1', props: { title: 'T' } }, out: [{ type: 'alm@satisfies', to: { id: 'n2' } }] });
    expect(changes).toEqual([
      { kind: 'state', op: 'added', old: undefined, new: 'draft' },
      { kind: 'property', name: 'title', op: 'added', new: 'T' },
      { kind: 'link', name: 'alm@satisfies', target: 'n2', op: 'added' },
    ]);
  });

  it('removes a link the draft no longer holds', () => {
    expect(nodeChanges(base, { node: base.node, out: [base.out[1]] })).toEqual([{ kind: 'link', name: 'alm@satisfies', target: 'n2', op: 'removed' }]);
  });
});
