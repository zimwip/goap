import { describe, expect, it } from 'vitest';
import { combinedCel, exprOf, inspectBoundary, inspectCondition, inspectLevel, inspectStep, inspectionText, type FlowFacts } from './flowInspect';
import type { LevelCheck, ProcessGraph } from './api';

const graph: ProcessGraph = {
  process: 'dev',
  steps: [
    { path: 'dev/analyse', name: 'analyse', method: 'action', target: 'analyse_it', description: 'Analyse the impact', entry: { accepted: true }, exit: { analysed: true }, roles: { responsible: 'analyst' } },
    { path: 'dev/build', name: 'build', method: 'method', capability: 'building', entry: { analysed: true, blocked: false }, exit: { built: true }, foreach: 'change.impacts' },
  ],
  methods: [{ name: 'go_build', for: 'building', when: 'change.namespace == "alm"', priority: 2 }],
  agents: [{ name: 'dev', actions: [{ name: 'analyse_it', kind: 'llm', pre: { accepted: true }, effects: { analysed: true } }] }],
};
const level: LevelCheck = {
  path: 'dev',
  kind: 'process',
  agent: 'dev',
  goal: 'dev',
  inputs: {},
  outputs: { built: true },
  ok: false,
  order: [{ name: 'analyse', layer: 0 }, { name: 'build', layer: 1 }],
  gaps: [{ step: 'build', kind: 'blocked', missing: ['blocked'], message: 'build needs blocked false' }],
  steps: [
    { path: 'dev/analyse', name: 'analyse', entry: { accepted: true }, exit: { analysed: true } },
    { path: 'dev/build', name: 'build', entry: { analysed: true, blocked: false }, exit: { built: true } },
  ],
};
const facts: FlowFacts = {
  graph,
  levels: [level],
  conditions: [
    { name: 'accepted', expr: 'change.state == "accepted"' },
    { name: 'blocked', expr: 'size(risks) > 0' },
  ],
  plan: { actions: [{ name: 'analyse_it', step: 'dev/analyse', kind: 'llm', cost: 1 }] },
  issues: [{ path: 'processes[0]', activity: 'dev/build', message: 'no method applies' }],
};
const section = (i: { sections: { title: string }[] }, title: string) => i.sections.find((s) => s.title === title) as never as { rows?: { label: string; value?: string; code?: string }[]; code?: string; text?: string };

describe('the CEL of conditions', () => {
  it('reads the expression of a dynamic condition, none for a step-made one', () => {
    expect(exprOf(facts, 'accepted')).toBe('change.state == "accepted"');
    expect(exprOf(facts, 'analysed')).toBeUndefined();
  });

  it('joins an entry into one expression, negating what is wanted false, keeping unknown names', () => {
    const r = combinedCel(facts, { analysed: true, blocked: false });
    expect(r.code).toBe('analysed\n&& !(size(risks) > 0)');
    expect(r.unknown).toEqual(['analysed']);
    expect(combinedCel(facts, { accepted: true }).code).toBe('change.state == "accepted"');
    expect(combinedCel(facts, {}).code).toBe('true');
  });
});

describe('inspections', () => {
  it('a condition: its CEL, its origin at the level, who sets and needs it', () => {
    const i = inspectCondition(facts, 'blocked', level);
    expect(section(i, 'Expression (CEL)').code).toBe('size(risks) > 0');
    expect(section(i, 'At level dev').rows?.[0].value).toBe('read from the state of the change');
    expect(section(i, 'Needed by the steps').rows?.map((r) => r.label)).toEqual(['dev/build']);
    expect(section(i, 'Set by the steps').text).toBe('No step of this flow sets it.');
    const a = inspectCondition(facts, 'analysed', level);
    expect(section(a, 'Expression (CEL)').text).toContain('not a dynamic condition');
    expect(section(a, 'Actions of the agents').rows?.[0]).toMatchObject({ label: 'dev/analyse_it', value: 'effect true · llm' });
  });

  it('a step: how it runs, its RACI, conditions with CEL, plan, gaps and rules', () => {
    const s = inspectStep(facts, 'dev/analyse');
    expect(section(s, 'Overview').rows).toContainEqual({ label: 'Runs as', value: 'action analyse_it' });
    expect(section(s, 'Responsibilities (RACI)').rows).toEqual([{ label: 'Responsible', value: 'analyst' }]);
    expect(section(s, 'Entry conditions').rows?.[0].code).toBe('change.state == "accepted"');
    expect(section(s, 'In the plan').rows?.[0]).toMatchObject({ label: 'analyse_it', value: 'llm · cost 1' });
    const b = inspectStep(facts, 'dev/build');
    expect(section(b, 'Overview').rows).toContainEqual({ label: 'For each', code: 'change.impacts' });
    expect(section(b, 'Methods providing the capability').rows?.[0]).toMatchObject({ label: 'go_build', code: 'change.namespace == "alm"' });
    expect(section(b, 'Gaps').rows?.[0].value).toBe('build needs blocked false');
    expect(section(b, 'Rules broken').rows?.[0].value).toBe('no method applies');
  });

  it('the conditions of a step only', () => {
    const s = inspectStep(facts, 'dev/build', 'conditions');
    expect(s.sections.map((x) => x.title)).toEqual(['Entry conditions', 'Entry as one CEL expression', 'Exit criteria', 'Exit as one CEL expression']);
  });

  it('a boundary block and the level', () => {
    const b = inspectBoundary(facts, 'Change state', [['accepted', true], ['blocked', true]], level);
    expect(section(b, 'Conditions').rows?.[1]).toMatchObject({ label: 'blocked', value: 'needed by build', code: 'size(risks) > 0' });
    const l = inspectLevel(facts, level);
    expect(section(l, 'Order the conditions allow').rows).toEqual([{ label: '1', value: 'analyse' }, { label: '2', value: 'build' }]);
    expect(inspectionText(l)).toContain('## Gaps');
  });
});
