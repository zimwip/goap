import { describe, expect, it } from 'vitest';
import { emptyForm, fromForm, toForm } from './methodologyForm';
import type { Methodology } from './api';

// The composition of a methodology (ADR 0106) survives the editor: saving from the web keeps its lifecycle, what it
// integrates, its guardian and where its events come from.
describe('the composition of a methodology form', () => {
  const m: Methodology = {
    name: 'release',
    version: '1',
    lifecycle: 'delivery',
    criticality: 'C1',
    integrates: [{ methodologies: ['sdlc', 'hotfix'], exclusive: true, attach: 'auto' }, { methodologies: ['docs'] }],
    guardian: { commit: 'integrator.state == "integrating"', transitions: { ready: 'integrator.attached' } },
    on: [{ event: 'change.committed', from: 'integrated' }, { event: 'step.completed', filter: 'true' }],
  };

  it('round trips through the form', () => {
    const out = fromForm(toForm(m)).methodology;
    expect(out.lifecycle).toBe('delivery');
    expect(out.criticality).toBe('C1');
    expect(out.integrates).toEqual(m.integrates);
    expect(out.guardian).toEqual(m.guardian);
    expect(out.on).toEqual(m.on);
  });

  it('writes the defaults as nothing', () => {
    const f = toForm({ name: 'm', integrates: [{ methodologies: ['a'], attach: 'decision' }], on: [{ event: 'change.committed', from: 'self' }] });
    expect(f.integrates[0].attach).toBe('');
    expect(f.on[0].from).toBe('');
    const out = fromForm(f).methodology;
    expect(out.integrates).toEqual([{ methodologies: ['a'] }]);
    expect(out.on).toEqual([{ event: 'change.committed' }]);
  });

  it('leaves out an empty guardian and empty rows', () => {
    const f = emptyForm();
    f.integrates.push({ methodologies: ' ', exclusive: false, attach: '' });
    f.guardianTransitions.push({ name: 'ready', expr: '' });
    const out = fromForm(f).methodology;
    expect(out.guardian).toBeUndefined();
    expect(out.integrates).toBeUndefined();
    expect(out.lifecycle).toBeUndefined();
  });
});
