import { describe, expect, it } from 'vitest';
import { fromForm, toForm } from './methodologyForm';
import { eventIssues, unknownEvent } from './subscriptions';
import type { Methodology } from './api';

const eventful: Methodology = {
  name: 'eventful',
  version: '1.0.0',
  namespace: 'alm',
  events: [{ name: 'spec.ready', description: 'The spec is written' }],
  actions: [
    { name: 'write', kind: 'human', effects: { written: true }, emits: ['spec.ready'] },
    { name: 'notify', kind: 'human', effects: { notified: true }, on: [{ event: 'spec.ready' }] },
  ],
  processes: [{ name: 'review', on: [{ event: 'change.created' }], steps: [{ name: 'rate', action: 'write', on: [{ event: 'change.edited', filter: '"intent" in event.fields' }] }] }],
  methods: [{ name: 'm', for: 'cap', on: [{ event: 'spec.ready', from: 'integrated' }] }],
};

describe('subscriptions', () => {
  it('keeps the on of every activity, the declared events and the emits through the form', () => {
    const { methodology, issues } = fromForm(toForm(eventful));
    expect(issues).toEqual([]);
    expect(methodology.events).toEqual(eventful.events);
    expect(methodology.actions?.[0].emits).toEqual(['spec.ready']);
    expect(methodology.actions?.[1].on).toEqual([{ event: 'spec.ready' }]);
    expect(methodology.processes?.[0].on).toEqual([{ event: 'change.created' }]);
    expect(methodology.processes?.[0].steps?.[0].on).toEqual([{ event: 'change.edited', filter: '"intent" in event.fields' }]);
    expect(methodology.methods?.[0].on).toEqual([{ event: 'spec.ready', from: 'integrated' }]);
  });

  it('flags an event the methodology neither knows nor declares, as the compiler does', () => {
    expect(unknownEvent('change.edited', [])).toBe('');
    expect(unknownEvent('spec.ready', ['spec.ready'])).toBe('');
    expect(unknownEvent('spec.ready', [])).toContain('unknown event');
    const f = toForm({ ...eventful, events: [] });
    f.actions[0].emits = 'spec.ready';
    const paths = fromForm(f).issues.map((i) => i.path);
    expect(paths.sort()).toEqual(['actions[0].emits[0]', 'actions[1].on[0].event', 'methods[0].on[0].event']);
  });

  it('checks the declared names', () => {
    const paths = eventIssues([{ name: 'Bad Name', description: '' }, { name: 'change.mine', description: '' }, { name: 'a.b', description: '' }, { name: 'a.b', description: '' }]).map((i) => i.path);
    expect(paths).toEqual(['events[0].name', 'events[1].name', 'events[3].name']);
  });
});
