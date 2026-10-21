import { describe, expect, it } from 'vitest';
import { hoodIn, indexOf } from './graphIndex';

describe('hoodIn', () => {
  // the world of a change: REQ-1 drafted (version 0), TST-1 left alone, its link naming the version REQ-1 was checked
  // out from; NEED-1 committed at v2 by the change, REQ-2's link still naming v1
  const world = indexOf(
    'B1',
    [
      { id: 'req', key: 'REQ-1', version: 0 },
      { id: 'need', key: 'NEED-1', version: 2 },
      { id: 'req2', key: 'REQ-2', version: 1 },
      { id: 'tst', key: 'TST-1', version: 1 },
      { id: 'new', key: 'REQ-9', version: 0 },
    ],
    [
      { id: 'l1', type: 'satisfies', from: { id: 'req', version: 0 }, to: { id: 'need', version: 2 } },
      { id: 'l4', type: 'satisfies', from: { id: 'req2', version: 1 }, to: { id: 'need', version: 1 } },
      { id: 'l2', type: 'verifies', from: { id: 'tst', version: 1 }, to: { id: 'req', version: 2 } },
      { id: 'l3', type: 'refines', from: { id: 'new', version: 0 }, to: { id: 'req', version: 0 } },
    ],
  );

  it('reads the links both ways and the nodes at their ends', () => {
    const h = hoodIn(world, 'req');
    expect(h.node?.key).toBe('REQ-1');
    expect(h.links.map((l) => l.id).sort()).toEqual(['l1', 'l2', 'l3']);
    expect(h.nodes.map((n) => n.key).sort()).toEqual(['NEED-1', 'REQ-9', 'TST-1']);
  });

  it('flags a link naming another version of its target than the world holds, not one to a draft', () => {
    expect(hoodIn(world, 'need').suspectLinkIds).toEqual(['l4']);
    expect(hoodIn(world, 'req').suspectLinkIds).toEqual([]);
  });

  it('knows nothing of a node out of the world', () => {
    expect(hoodIn(world, 'gone')).toEqual({ nodes: [], links: [], suspectLinkIds: [] });
  });
});
