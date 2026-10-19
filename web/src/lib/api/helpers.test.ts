import { describe, expect, it } from 'vitest';
import { decodeLogEntry } from './helpers';
import type { ImpactEvent } from './types/graph';

describe('decodeLogEntry', () => {
  it('puts the envelope of an impact event back from the columns', () => {
    const e = decodeLogEntry<ImpactEvent>({
      seq: '7', id: 'e1', changeId: 'c1', type: 'impact.proposed', subject: 'i1', flow: 'f1', by: 'alice', at: '2026-01-01T00:00:00Z',
      payload: JSON.stringify({ op: 'proposed', state: { key: 'K' } }),
    });
    expect(e).toMatchObject({ id: 'e1', changeId: 'c1', impactId: 'i1', flow: 'f1', by: 'alice', op: 'proposed', state: { id: 'i1', key: 'K' } });
  });
  it('leaves a fact as stored', () => {
    expect(decodeLogEntry<{ id: string }>({ id: 'x', type: 'fact.artifact', payload: '{"id":"a"}' })).toEqual({ id: 'a' });
  });
});
