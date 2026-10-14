import { describe, expect, it, vi } from 'vitest';

vi.mock('../shell/tabs.svelte', () => ({ activeTab: () => ({ id: 't1' }) }));

import { describeTools, runTool, screenContext } from '../assist/registry.svelte';
import { fieldShape, offerForm } from './assist';
import type { FormDef, FormValues } from './types';

const form: FormDef = {
  fields: [
    { name: 'decision', widget: 'select', required: true, constraints: { options: ['go', 'stop'] } },
    { name: 'why', widget: 'textarea' },
    { name: 'intro', widget: 'markdown', constraints: { text: 'x' } },
  ],
};

describe('declared forms offered to the assistant', () => {
  it('says what each value looks like', () => {
    expect(fieldShape(form.fields![0])).toBe('decision*: one of go|stop');
    expect(fieldShape({ name: 'l', widget: 'link', constraints: { linkTypes: ['a', 'b'] } })).toBe('l: list of {to: node key, type}');
  });

  it('is a form entity, filled and submitted through the tools', async () => {
    let values: FormValues = {};
    const sent: FormValues[] = [];
    const off = offerForm({
      id: 'task:1',
      label: 'Decide',
      form: () => form,
      issues: (): Record<string, string[]> => (values.decision ? {} : { decision: ['required'] }),
      values: () => values,
      fill: (v) => (values = { ...values, ...v }),
      submit: () => (values.decision ? void sent.push(values) : 'The form has issues'),
    });
    expect(describeTools('t1').map((t) => t.name)).toEqual(expect.arrayContaining(['fill_form', 'submit_form']));
    const e = screenContext('t1').entities?.find((x) => x.type === 'form');
    expect(e?.props).toMatchObject({ fields: 'decision*: one of go|stop; why: textarea', issues: 'decision: required' });
    expect((await runTool('fill_form', { form: 'task:1', values: '{"nope": 1}' }, 't1')).ok).toBe(false);
    expect((await runTool('submit_form', { form: 'task:1' }, 't1')).ok).toBe(false);
    expect((await runTool('fill_form', { form: 'task:1', values: '{"decision": "go"}' }, 't1')).ok).toBe(true);
    expect((await runTool('submit_form', { form: 'task:1' }, 't1')).ok).toBe(true);
    expect(sent).toEqual([{ decision: 'go' }]);
    off();
    expect(describeTools('t1').map((t) => t.name)).not.toContain('fill_form');
  });
});
