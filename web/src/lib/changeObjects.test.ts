import { describe, expect, it } from 'vitest';
import type { ChangeObject, ChangeObjectTypeInfo } from './api';
import { asksKey, createWrite, editWrite, keyLabel, objectTabs, objectsOfTypes, transitionsOf } from './changeObjects';

// the tabs of the change objects of a change (ADR 0098): declared ones first, then one per type held
describe('changeObjects', () => {
  const objects: ChangeObject[] = [
    { type: 'risks@Risk', key: 'RISK-1', state: 'open' },
    { type: 'execution@Fact', key: 'FACT-1' },
    { type: 'execution@Run', key: 'p1' },
    { type: 'risks@Risk', key: 'RISK-2', state: 'closed' },
  ];

  it('lists the declared tabs, then a tab per type the change holds', () => {
    const tabs = objectTabs(objects, [{ title: 'Risks', editor: 'risk-register', objects: ['risks@Risk', 'risks@Action'] }, { objects: ['decisions@Point'] }], (t) =>
      t === 'execution@Run' ? 'runs' : '',
    );
    expect(tabs.map((t) => [t.id, t.title, t.editor, t.count, t.declared])).toEqual([
      ['objects:0', 'Risks', 'risk-register', 2, true],
      ['objects:1', 'Point', '', 0, true],
      ['objects:execution@Fact', 'Fact', '', 1, false],
      ['objects:execution@Run', 'Run', 'runs', 1, false],
    ]);
  });

  it('adds a tab for a type the person starts', () => {
    expect(objectTabs([], [], undefined, ['risks@Risk']).map((t) => t.id)).toEqual(['objects:risks@Risk']);
  });

  it('selects, names and writes change objects', () => {
    expect(objectsOfTypes(objects, ['risks@Risk']).map((o) => o.key)).toEqual(['RISK-1', 'RISK-2']);
    expect(keyLabel({ type: 'execution@State', key: '' })).toBe('State');
    expect(createWrite('risks@Risk', { title: 't' })).toEqual({ type: 'risks@Risk', value: { title: 't' } });
    expect(editWrite({ type: 'risks@Risk', key: 'RISK-1', workspace: 'w1' }, { title: 'u' }, 'close')).toEqual({
      type: 'risks@Risk',
      key: 'RISK-1',
      workspace: 'w1',
      merge: true,
      value: { title: 'u' },
      transition: 'close',
    });
  });

  it('knows the keys to ask and the transitions to offer', () => {
    const risk: ChangeObjectTypeInfo = {
      ref: 'risks@Risk',
      key: { kind: 'sequence', prefix: 'RISK' },
      lifecycle: { initial: 'open', transitions: [{ name: 'close', from: 'open', to: 'closed' }, { name: 'reopen', from: 'closed', to: 'open' }] },
    };
    expect(asksKey(risk)).toBe(false);
    expect(asksKey({ key: { kind: 'ref', ref: 'impact' } })).toBe(true);
    expect(transitionsOf(risk, 'open')).toEqual([{ name: 'close', to: 'closed' }]);
    expect(transitionsOf(risk, '')).toEqual([]);
  });
});
