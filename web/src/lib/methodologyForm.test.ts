import { describe, expect, it } from 'vitest';
import { fromForm, toForm } from './methodologyForm';
import type { Methodology } from './api';

// What the editor sends back (the flow, the checks, a save) must be what it was given: a field the form drops changes
// the methodology behind the user's back (a scoped condition losing its scope no longer compiles, ADR 0109).
describe('the methodology form', () => {
  it('keeps scoped conditions, aggregates, scoped actions, their params and the subjects of a step', () => {
    const m: Methodology = {
      name: 'm',
      version: '1.0.0',
      conditions: [
        { name: 'suspected', scope: 'impact', description: 'found by propagation', expr: 'self.rationale.startsWith("propagated (")' },
        { name: 'confirmed', scope: 'impact', expr: 'assessments.exists(a, a.key == "impact:" + self.key)' },
        { name: 'impacts_confirmed', all: 'confirmed', where: 'self.conditions.suspected', empty: true },
        { name: 'some_confirmed', any: 'confirmed' },
      ],
      actions: [
        { name: 'qualify', kind: 'llm', prompt: 'qualify it', effects: { impacts_confirmed: true }, scope: 'impact', where: 'self.conditions.suspected', parallel: 2 },
        { name: 'build_java', kind: 'human', instructions: 'build', specializes: 'build', when: 'true', params: { technology: 'java' } },
      ],
      processes: [{ name: 'p', steps: [{ name: 'q', action: 'qualify', where: 'self.conditions.suspected' }] }],
    };
    const back = fromForm(toForm(m)).methodology;
    expect(back.conditions).toEqual(m.conditions);
    expect(back.actions?.[0]).toMatchObject({ scope: 'impact', where: 'self.conditions.suspected', parallel: 2 });
    expect(back.actions?.[1].params).toEqual({ technology: 'java' });
    expect(back.processes?.[0].steps?.[0].where).toBe('self.conditions.suspected');
  });

  it('keeps the assessment views and refuses an invalid JSON (ADR 0116)', () => {
    const assessments = [{ subject: 'description', states: [{ conditions: { description_accepted: true }, tone: 'ok', label: 'accepted' }, { tone: 'warn', open: true }] }];
    const m: Methodology = { name: 'm', version: '1.0.0', assessments };
    expect(fromForm(toForm(m)).methodology.assessments).toEqual(assessments);
    const f = toForm(m);
    f.assessments = '{"subject": "x"}';
    expect(fromForm(f).issues.map((i) => i.path)).toContain('assessments');
  });
});
