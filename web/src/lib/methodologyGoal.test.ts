import { describe, expect, it } from 'vitest';
import { emptyForm, fromForm, goalChoices, toForm } from './methodologyForm';

describe('the main goal of a methodology form', () => {
  it('round trips through the form', () => {
    const f = toForm({ name: 'm', version: '1', goal: 'deliver', goals: [{ name: 'deliver', pre: { done: true } }] });
    expect(f.goal).toBe('deliver');
    expect(fromForm(f).methodology.goal).toBe('deliver');
    expect(fromForm(emptyForm()).methodology.goal).toBeUndefined();
  });
  it('offers the goals, then the processes, once each', () => {
    const f = toForm({ name: 'm', goals: [{ name: 'a', pre: {} }, { name: 'p', pre: {} }], processes: [{ name: 'p' }, { name: 'q' }] });
    expect(goalChoices(f)).toEqual(['a', 'p', 'q']);
  });
});
