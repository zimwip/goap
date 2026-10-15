import { describe, expect, it } from 'vitest';
import { fieldIssues, formIssues, initialValues, linkTargets, nodeChoices, sections, submittedValues } from './validate';
import type { FormDef, NodeSource } from './types';

const source: NodeSource = {
  nodes: [
    { key: 'REQ-1', type: 'alm@Requirement', state: 'approved' },
    { key: 'REQ-2', type: 'alm@Requirement', state: 'draft', inChange: true },
    { key: 'APP-1', type: 'alm@WebApplication' },
    { key: 'CMP-1', type: 'alm@Component' },
  ],
  isA: (t, a) => t === a || (t === 'alm@WebApplication' && a === 'alm@Application'),
};

const form: FormDef = {
  fields: [
    { name: 'intro', widget: 'markdown', constraints: { text: 'Pick' } },
    { name: 'requirement', widget: 'node', required: true, section: 'What', constraints: { types: ['alm@Requirement'], states: ['approved'] } },
    { name: 'realizedBy', widget: 'link', section: 'What', constraints: { from: 'requirement', linkTypes: ['alm@realized_by'], toTypes: ['alm@Application'], max: 1 } },
    { name: 'level', widget: 'select', default: 'low', constraints: { options: ['low', 'high'] } },
    { name: 'why', widget: 'textarea', required: true, constraints: { minLength: 3 } },
  ],
};

describe('forms', () => {
  it('starts from the defaults and groups the fields by section', () => {
    expect(initialValues(form)).toEqual({ level: 'low' });
    expect(sections(form).map((s) => [s.name, s.fields.length])).toEqual([
      ['', 3],
      ['What', 2],
    ]);
  });

  it('offers only the nodes the constraints allow', () => {
    expect(nodeChoices(form.fields![1], source).map((n) => n.key)).toEqual(['REQ-1']);
    expect(linkTargets(form.fields![2], source).map((n) => n.key)).toEqual(['APP-1']);
    expect(nodeChoices({ name: 'x', widget: 'node', constraints: { inChange: true } }, source).map((n) => n.key)).toEqual(['REQ-2']);
  });

  it('checks the values like the engine', () => {
    const values = { requirement: 'REQ-2', realizedBy: [{ to: 'CMP-1' }, { to: 'APP-1' }], why: 'no' };
    const is = formIssues(form, values, source);
    expect(is['requirement']).toEqual(['REQ-2 is not allowed here']);
    expect(is['realizedBy']).toEqual(['link 1: CMP-1 is not allowed here', 'at most 1 links']);
    expect(is['why']).toEqual(['at least 3 characters']);
    expect(is['level']).toBeUndefined();
    expect(fieldIssues(form.fields![1], undefined, {}, source)).toEqual(['required']);
  });

  it('sends the links with their source and type, without the blanks', () => {
    const out = submittedValues(form, { requirement: 'REQ-1', realizedBy: [{ to: 'APP-1' }], why: 'okay', level: '' });
    expect(out).toEqual({ requirement: 'REQ-1', realizedBy: [{ from: 'REQ-1', type: 'alm@realized_by', to: 'APP-1' }], why: 'okay' });
    expect(formIssues(form, { ...out, level: 'low' }, source)).toEqual({});
  });
});

describe('conditional fields and groups', () => {
  const cond: FormDef = {
    fields: [
      { name: 'decision', widget: 'select', required: true, constraints: { options: ['accept', 'reject'] } },
      { name: 'reason', widget: 'textarea', required: true, visibleWhen: [{ field: 'decision', equals: 'reject' }] },
      { name: 'tags', widget: 'multiselect', constraints: { options: ['a', 'b'] } },
      { name: 'note', widget: 'text', requiredWhen: [{ field: 'tags', in: ['b'] }] },
      {
        name: 'risks',
        widget: 'group',
        constraints: { max: 1 },
        fields: [
          { name: 'title', widget: 'text', required: true },
          { name: 'mitigation', widget: 'text', visibleWhen: [{ field: 'title', set: true }], required: true },
        ],
      },
    ],
  };

  it('hides, requires and drops by the conditions', () => {
    expect(formIssues(cond, { decision: 'accept', reason: '' }, source)).toEqual({});
    expect(submittedValues(cond, { decision: 'accept', reason: 'x' })).toEqual({ decision: 'accept' });
    const is = formIssues(cond, { decision: 'reject', tags: ['b'] }, source);
    expect(is['reason']).toEqual(['required']);
    expect(is['note']).toEqual(['required']);
  });

  it('checks the entries of a group', () => {
    const is = formIssues(cond, { decision: 'accept', risks: [{ title: 'x' }, {}] }, source);
    expect(is['risks']).toEqual(['entry 1, mitigation: required', 'entry 2, title: required', 'at most 1 entries']);
    expect(submittedValues(cond, { decision: 'accept', risks: [{ title: 'x', mitigation: 'y', extra: '' }] })).toEqual({
      decision: 'accept',
      risks: [{ title: 'x', mitigation: 'y' }],
    });
  });
});

describe('new node, decision and file fields', () => {
  const f: FormDef = {
    fields: [
      { name: 'req', widget: 'newNode', constraints: { types: ['alm@Requirement'], properties: ['title'] } },
      { name: 'links', widget: 'link', constraints: { from: 'req', linkTypes: ['alm@verifies'] } },
      { name: 'choice', widget: 'decision', constraints: { justify: true } },
      { name: 'doc', widget: 'file', constraints: { accept: ['.csv'], maxBytes: 4 } },
    ],
  };
  const withPoints: NodeSource = { ...source, decisions: [{ id: 'D1', question: 'ship?', options: [{ id: 'go', label: 'Go' }] }, { id: 'D2', question: 'old', options: [], decided: true }] };

  it('accepts a created node as the source of links', () => {
    const v = { req: { key: 'REQ-9', type: 'alm@Requirement', props: { title: 'x' } }, links: [{ to: 'CMP-1' }], choice: { point: 'D1', option: 'go', justification: 'ok' } };
    expect(formIssues(f, v, withPoints)).toEqual({});
    expect(submittedValues(f, v)['links']).toEqual([{ from: 'REQ-9', type: 'alm@verifies', to: 'CMP-1' }]);
  });

  it('refuses what the engine refuses', () => {
    const is = formIssues(
      f,
      { req: { key: 'REQ-1', type: 'alm@Component', props: { owner: 'x' } }, choice: { point: 'D2', option: 'go', justification: 'j' }, doc: { name: 'a.pdf', size: 2, content: 'YQ==' } },
      withPoints,
    );
    expect(is['req']).toEqual(['alm@Component is not one of alm@Requirement', 'REQ-1 already exists', 'owner is not a property asked']);
    expect(is['choice']).toEqual(['the decision point "old" is already decided']);
    expect(is['doc']).toEqual(['a.pdf is not one of .csv']);
  });
});

describe('the form of a node type', () => {
  it('turns the values into properties and links from the node', async () => {
    const { nodeWrite, SELF_KEY } = await import('./nodeForm');
    const form: FormDef = {
      fields: [
        { name: 'title', widget: 'text', required: true },
        { name: 'intro', widget: 'markdown', constraints: { text: 'x' } },
        { name: 'satisfies', widget: 'link', constraints: { fromKey: SELF_KEY, linkTypes: ['alm@satisfies'] } },
      ],
    };
    expect(nodeWrite(form, { title: 'T', satisfies: [{ to: 'NEED-1' }] })).toEqual({ props: { title: 'T' }, links: [{ type: 'alm@satisfies', to: 'NEED-1' }] });
  });
});
