import { describe, expect, it } from 'vitest';
import { lifecycleRows } from './lifecycle';
import type { Lifecycle } from './api';
import type { TypeCatalog } from './stores/types.svelte';

// A state is landable unless flagged notLandable (ADR 0078): the row of a node says whether it can land as it stands.
const lc: Lifecycle = {
  initial: 'draft',
  states: [{ name: 'draft', notLandable: true }, { name: 'approved' }],
  transitions: [{ name: 'approve', from: 'draft', to: 'approved' }],
};
const cat = { lifecycle: (t?: string) => (t === 'Req' ? lc : undefined), properties: () => [] } as unknown as TypeCatalog;

describe('lifecycleRows', () => {
  it('marks a node in a notLandable state, and only that one', () => {
    const nodes = [
      { id: 'a', key: 'REQ-A', type: 'Req', version: 1, state: 'draft' },
      { id: 'b', key: 'REQ-B', type: 'Req', version: 1, state: 'approved' },
      { id: 'c', key: 'NOTE-C', type: 'Note', version: 1, state: '' },
    ];
    const rows = lifecycleRows(cat, nodes, nodes.map((n) => ({ id: n.id })), [], new Map(), []);
    const landable = Object.fromEntries(rows.map((r) => [r.node.key, r.landable]));
    expect(landable).toEqual({ 'REQ-A': false, 'REQ-B': true, 'NOTE-C': true });
  });
});
