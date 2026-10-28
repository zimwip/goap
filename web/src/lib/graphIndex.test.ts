import { describe, expect, it } from 'vitest';
import { hoodIndex } from './graphIndex';

describe('hoodIndex', () => {
  // two neighbourhoods read in one world: REQ-1 (linked to NEED-1, verified by TST-1), then NEED-1 (also satisfied by
  // REQ-2)
  const req = {
    node: { id: 'req', key: 'REQ-1', version: 0 },
    nodes: [
      { id: 'need', key: 'NEED-1', version: 2 },
      { id: 'tst', key: 'TST-1', version: 1 },
    ],
    links: [
      { id: 'l1', type: 'satisfies', from: { id: 'req', version: 0 }, to: { id: 'need', version: 2 } },
      { id: 'l2', type: 'verifies', from: { id: 'tst', version: 1 }, to: { id: 'req', version: 2 } },
    ],
  };
  const need = {
    node: { id: 'need', key: 'NEED-1', version: 2 },
    nodes: [
      { id: 'req', key: 'REQ-1', version: 0 },
      { id: 'req2', key: 'REQ-2', version: 1 },
    ],
    links: [
      { id: 'l1', type: 'satisfies', from: { id: 'req', version: 0 }, to: { id: 'need', version: 2 } },
      { id: 'l4', type: 'satisfies', from: { id: 'req2', version: 1 }, to: { id: 'need', version: 1 } },
    ],
  };

  it('holds the nodes read and the links between them, once each', () => {
    const ix = hoodIndex('B1', [req, need]);
    expect(ix.commitId).toBe('B1');
    expect([...ix.nodes.keys()].sort()).toEqual(['need', 'req', 'req2', 'tst']);
    expect(ix.out.get('req')?.map((l) => l.id)).toEqual(['l1']);
    expect(ix.in.get('need')?.map((l) => l.id).sort()).toEqual(['l1', 'l4']);
    expect(ix.in.get('req')?.map((l) => l.id)).toEqual(['l2']);
  });

  it('knows which nodes have all their links: the ones whose neighbourhood was read', () => {
    expect([...hoodIndex('B1', [req]).loaded]).toEqual(['req']);
    expect([...hoodIndex('B1', [req, need]).loaded].sort()).toEqual(['need', 'req']);
  });

  it('is empty with nothing read', () => {
    const ix = hoodIndex('B1', [{}]);
    expect(ix.list).toEqual([]);
    expect(ix.loaded.size).toBe(0);
  });
});
