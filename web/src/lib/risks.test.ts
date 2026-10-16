import { describe, expect, it } from 'vitest';
import type { ChangeItem } from './api';
import { actionList, covers, pocData, riskRegister, unmitigated } from './risks';

const item = (type: string, data: Record<string, unknown>, id = String(Math.random())): ChangeItem => ({ id, type, data }) as ChangeItem;

describe('proofs of concept on the register (ADR 0131)', () => {
  const risk = item('risk', { key: 'RSK-1', title: 'the PSP does not split', probability: 4, impact: 4, status: 'mitigating' });
  const poc = item('action', pocData('ACT-1', 'split on the sandbox', 'derisk', ['RSK-1'], []));

  it('a running POC covers its risks', () => {
    const [a] = actionList([risk, poc]);
    expect(a.kind).toBe('poc');
    expect(a.purpose).toBe('derisk');
    expect(a.for).toBe('RSK-1');
    expect(covers(a, 'RSK-1')).toBe(true);
    expect(unmitigated(riskRegister([risk])[0], [a])).toBe(false);
  });

  it('a concluded POC covers nothing: a confirmed risk needs another mitigation', () => {
    const done = item('action', { key: 'ACT-1', status: 'done', change: 'c1' });
    const [a] = actionList([risk, poc, done]);
    expect(a.change).toBe('c1');
    expect(a.risks).toEqual(['RSK-1']);
    expect(covers(a, 'RSK-1')).toBe(false);
    expect(unmitigated(riskRegister([risk])[0], [a])).toBe(true);
  });

  it('a prototype carries its statements and covers no risk', () => {
    const [a] = actionList([item('action', pocData('ACT-2', 'checkout mock-up', 'explore', [], ['the voucher is validated in place']))]);
    expect(a.purpose).toBe('explore');
    expect(a.hypotheses).toEqual(['the voucher is validated in place']);
    expect(a.for).toBe('');
  });
});
