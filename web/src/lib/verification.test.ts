import { describe, expect, it } from 'vitest';
import type { ChangeItem, Struct } from './api';
import { closing, derogationRegister, expiredDerogation, expiryOf, verifications } from './verification';
import { criticalityOf, lowers, selectable } from './criticality';

const v = (data: Struct, extra: Partial<ChangeItem> = {}): ChangeItem => ({ kind: 'verification', data, ...extra });

describe('verifications', () => {
  it('folds the entries of an effect into its last state', () => {
    const items = [
      v({ state: 'produced', action: 'write', impacts: ['i1', 'i2'], oracle: 'human', independent: true }, { producedBy: 'agent', execution: 'e1' }),
      v({ state: 'verified', action: 'check', impact: 'i1', by: 'tool' }),
      v({ state: 'accepted', action: 'check', impact: 'i1', by: 'alice' }),
      v({ state: 'accepted_with_reserve', action: 'check', impact: 'i2', derogation: 'DRG-1' }),
    ];
    const rows = verifications(items);
    expect(rows.map((r) => [r.impact, r.state, r.open, r.by, r.derogation])).toEqual([
      ['i1', 'accepted', false, 'alice', ''],
      ['i2', 'accepted_with_reserve', false, '', 'DRG-1'],
    ]);
    expect(rows[0]).toMatchObject({ producer: 'agent', oracle: 'human', independent: true, action: 'write' });
  });

  it('keeps an effect open until a verdict, and starts over when it is produced again', () => {
    const produced = v({ state: 'produced', action: 'write', impacts: ['i1'] });
    expect(verifications([produced]).map((r) => r.open)).toEqual([true]);
    expect(verifications([produced, v({ state: 'verified', impact: 'i1', action: 'c' })])[0].open).toBe(true);
    const rejected = [produced, v({ state: 'rejected', impact: 'i1', action: 'c' })];
    expect(verifications(rejected)[0]).toMatchObject({ state: 'rejected', open: false });
    expect(verifications([...rejected, produced])[0]).toMatchObject({ state: 'produced', open: true });
    expect(verifications([v({ state: 'accepted', impact: 'unknown', action: 'c' })])).toEqual([]);
  });
});

describe('derogations', () => {
  const d = (data: Struct): ChangeItem => ({ kind: 'derogation', data: { key: 'DRG-1', rule: 'docs', target: 't', reason: 'why', signatory: 'alice', expires: '2030-01-02', ...data } });

  it('keeps the current version of each key', () => {
    const rows = derogationRegister([d({}), d({ key: 'DRG-2' }), d({ status: 'closed' })]);
    expect(rows.map((r) => [r.key, r.status, r.versions])).toEqual([
      ['DRG-1', 'closed', 2],
      ['DRG-2', 'open', 1],
    ]);
  });

  it('reads a date as the end of that day, UTC, and tells an expired one', () => {
    const [open] = derogationRegister([d({})]);
    expect(expiryOf(open)).toBe(Date.parse('2030-01-02T23:59:59.999Z'));
    expect(expiredDerogation(open, Date.parse('2030-01-02T12:00:00Z'))).toBe(false);
    expect(expiredDerogation(open, Date.parse('2030-01-03T00:00:00Z'))).toBe(true);
    const [shut] = derogationRegister([d({ status: 'closed' })]);
    expect(expiredDerogation(shut, Date.parse('2031-01-01T00:00:00Z'))).toBe(false);
  });

  it('closes by restating the record, signed by who closes it', () => {
    const [r] = derogationRegister([d({})]);
    expect(closing(r, 'bob')).toEqual({ key: 'DRG-1', rule: 'docs', target: 't', reason: 'why', signatory: 'bob', expires: '2030-01-02', status: 'closed' });
  });
});

describe('criticality', () => {
  it('defaults to C2 and reads the data of the change', () => {
    expect(criticalityOf(undefined)).toBe('C2');
    expect(criticalityOf({ data: {} })).toBe('C2');
    expect(criticalityOf({ data: { criticality: 'C3' } })).toBe('C3');
    expect(criticalityOf({ data: { criticality: 'high' } })).toBe('C2');
  });

  it('offers to raise, and to lower only with the permission', () => {
    expect(lowers('C3', 'C1')).toBe(true);
    expect(lowers('C1', 'C3')).toBe(false);
    expect(selectable('C2', false)).toEqual(['C2', 'C3']);
    expect(selectable('C2', true)).toEqual(['C1', 'C2', 'C3']);
    expect(selectable('C3', false)).toEqual(['C3']);
  });
});
