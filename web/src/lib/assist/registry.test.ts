import { beforeEach, describe, expect, it, vi } from 'vitest';

const tab = vi.hoisted(() => ({ id: 't1' }));
vi.mock('../shell/tabs.svelte', () => ({ activeTab: () => ({ id: tab.id }) }));

import * as reg from './registry.svelte';
import { assistField, fieldOf, fieldsOfTab, hasFields, registerField, type FieldSpec } from './registry.svelte';

const el = (connected = true) => ({ isConnected: connected, getBoundingClientRect: () => ({}) }) as unknown as HTMLElement;
const spec = (id: string, over: Partial<FieldSpec> = {}): FieldSpec => ({ id, label: id, get: () => '', set: () => {}, ...over });

beforeEach(() => {
  tab.id = 't1';
});

describe('field registry', () => {
  it('registers a field under the tab it is shown in and removes it on destroy', () => {
    const a = assistField(el(), spec('title'));
    expect(fieldsOfTab('t1').map((f) => f.spec.id)).toEqual(['title']);
    expect(fieldsOfTab('t2')).toEqual([]);
    a.destroy();
    expect(fieldsOfTab('t1')).toEqual([]);
  });

  it('follows the active tab by default', () => {
    const a = assistField(el(), spec('x'));
    tab.id = 't2';
    expect(fieldsOfTab()).toEqual([]);
    expect(hasFields()).toBe(false);
    tab.id = 't1';
    expect(hasFields()).toBe(true);
    a.destroy();
  });

  it('replaces the spec on update, so the setter is the latest', () => {
    const first = vi.fn();
    const second = vi.fn();
    const a = assistField(el(), spec('x', { set: first }));
    a.update(spec('x', { set: second }));
    fieldOf('x', 't1')?.spec.set('v');
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledWith('v');
    a.destroy();
  });

  it('leaves out detached elements, and read-only fields do not count as fillable', () => {
    const a = registerField(el(false), spec('gone'), 't1');
    const b = registerField(el(), spec('key', { readOnly: true }), 't1');
    expect(fieldsOfTab('t1').map((f) => f.spec.id)).toEqual(['key']);
    expect(hasFields('t1')).toBe(false);
    a.destroy();
    b.destroy();
  });
});

describe('screen tools', () => {
  const { registerAssist, describeTools, runTool, hasTool, resetRegistry, screenContext, focusContext } = reg;
  beforeEach(() => {
    resetRegistry();
    tab.id = 't1';
  });

  it('registers for as long as the view lives and offers the descriptors of the catalog', () => {
    const off = registerAssist({ tools: [{ name: 'rename_change', run: () => {} }] });
    const [d] = describeTools();
    expect(d).toMatchObject({ name: 'rename_change', level: 'write', guidance: expect.any(String) });
    expect(d.args?.required).toEqual(['title']);
    expect(hasTool('rename_change')).toBe(true);
    off();
    expect(describeTools()).toEqual([]);
    expect(hasTool('rename_change')).toBe(false);
  });

  it('refuses a tool the catalog does not know', () => {
    expect(() => registerAssist({ tools: [{ name: 'format_disk', run: () => {} }] })).toThrow(/not a tool of the catalog/);
  });

  it('offers a tool only while it is enabled, and only on its own tab', () => {
    let on = false;
    const off = registerAssist({ tools: [{ name: 'update_intent', enabled: () => on, run: () => {} }] });
    expect(describeTools()).toEqual([]);
    on = true;
    expect(describeTools().map((t) => t.name)).toEqual(['update_intent']);
    tab.id = 't2';
    expect(describeTools()).toEqual([]);
    off();
  });

  it('lets a view adapt a descriptor to its state (an enum of the ids on screen)', () => {
    const off = registerAssist({
      tools: [
        {
          name: 'select_impact',
          describe: (b) => ({ ...b, args: { ...b.args, properties: { impactId: { type: 'enum', enum: ['I1', 'I2'] } } } }),
          run: () => {},
        },
      ],
    });
    expect(describeTools()[0].args?.properties?.impactId.enum).toEqual(['I1', 'I2']);
    off();
  });

  it('drops a descriptor the server would refuse instead of sending it', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const off = registerAssist({ tools: [{ name: 'select_impact', describe: (b) => ({ ...b, args: { properties: { 'bad name': { type: 'string' } } } }), run: () => {} }] });
    expect(describeTools()).toEqual([]);
    expect(warn).toHaveBeenCalled();
    off();
    warn.mockRestore();
  });

  it('runs a registered tool with checked arguments and reports a refusal as a message', async () => {
    const run = vi.fn();
    const off = registerAssist({ tools: [{ name: 'rename_change', run }] });
    expect(await runTool('rename_change', { title: 'New' })).toEqual({ ok: true });
    expect(run).toHaveBeenCalledWith({ title: 'New' });
    const bad = await runTool('rename_change', { title: 3 });
    expect(bad).toMatchObject({ ok: false, error: expect.stringContaining('must be a string') });
    expect(await runTool('rename_change', {})).toMatchObject({ ok: false, error: expect.stringContaining('missing argument title') });
    expect(await runTool('rename_change', { title: 'x', other: 1 })).toMatchObject({ ok: false });
    expect(run).toHaveBeenCalledTimes(1);
    off();
  });

  it('never runs a tool that is not registered (any more), and says so', async () => {
    const run = vi.fn();
    expect(await runTool('rename_change', { title: 'x' })).toMatchObject({ ok: false, error: expect.stringContaining('no longer offers') });
    const off = registerAssist({ tools: [{ name: 'rename_change', run }] });
    off();
    expect(await runTool('rename_change', { title: 'x' })).toMatchObject({ ok: false });
    let on = true;
    registerAssist({ tools: [{ name: 'update_intent', enabled: () => on, run }] });
    on = false;
    expect(await runTool('update_intent', { intent: 'x' })).toMatchObject({ ok: false, error: expect.stringContaining('not applicable') });
    expect(run).not.toHaveBeenCalled();
  });

  it('turns a returned or thrown message into a failure', async () => {
    registerAssist({
      tools: [
        { name: 'rename_change', run: () => 'The title is taken.' },
        {
          name: 'update_intent',
          run: () => {
            throw new Error('boom');
          },
        },
      ],
    });
    expect(await runTool('rename_change', { title: 'x' })).toEqual({ ok: false, error: 'The title is taken.' });
    expect(await runTool('update_intent', { intent: 'x' })).toEqual({ ok: false, error: 'boom' });
  });

  it('derives set_field from the fillable fields, with an enum of their ids', async () => {
    const set = vi.fn();
    const a = assistField(el(), spec('size', { type: 'number', set }));
    const b = assistField(el(), spec('status', { type: 'enum', enum: ['open', 'closed'], set }));
    const c = assistField(el(), spec('key', { readOnly: true }));
    const d = assistField(el(), spec('title', { tool: 'set_title' }));
    const [t] = describeTools();
    expect(t.name).toBe('set_field');
    expect(t.args?.properties?.field.enum).toEqual(['size', 'status']);
    expect(await runTool('set_field', { field: 'size', value: '12' })).toEqual({ ok: true });
    expect(set).toHaveBeenLastCalledWith(12);
    expect(await runTool('set_field', { field: 'size', value: 'abc' })).toMatchObject({ ok: false });
    expect(await runTool('set_field', { field: 'status', value: 'closed' })).toEqual({ ok: true });
    expect(await runTool('set_field', { field: 'status', value: 'weird' })).toMatchObject({ ok: false });
    expect(await runTool('set_field', { field: 'key', value: 'x' })).toMatchObject({ ok: false });
    [a, b, c, d].forEach((h) => h.destroy());
    expect(describeTools()).toEqual([]);
  });

  it('merges the screen and focus layers of the views of a tab and adds the fields as entities without values', () => {
    const off1 = registerAssist({ screen: () => ({ kind: 'change', title: 'T', summary: 'A.', entities: [{ type: 'impact', id: 'I1', label: 'REQ-1' }] }), focus: () => ({ errors: ['e1'], pendingAction: 'editing' }) });
    const off2 = registerAssist({ screen: () => ({ summary: 'B.' }), focus: () => ({ errors: ['e2'], dialogKind: 'edit' }) });
    const f = assistField(el(), spec('title', { type: 'string', get: () => 'SECRET VALUE', description: 'the title' }));
    const s = screenContext();
    expect(s).toMatchObject({ kind: 'change', title: 'T', summary: 'A. B.' });
    expect(s.entities?.map((e) => `${e.type}:${e.id}`)).toEqual(['impact:I1', 'field:title']);
    expect(JSON.stringify(s)).not.toContain('SECRET VALUE');
    expect(s.entities?.[1].props?.guidance).toBe('the title');
    expect(focusContext()).toEqual({ dialogKind: 'edit', pendingAction: 'editing', errors: ['e1', 'e2'] });
    off1();
    off2();
    f.destroy();
  });

  it('survives a provider that throws', () => {
    const off = registerAssist({
      screen: () => {
        throw new Error('x');
      },
    });
    expect(screenContext()).toEqual({});
    off();
  });

  it('points at a target and finds the entity of a focused element', () => {
    const calls: string[] = [];
    const node = { isConnected: true, scrollIntoView: () => calls.push('scroll'), classList: { add: () => calls.push('add'), remove: () => calls.push('remove') }, contains: () => false } as unknown as HTMLElement;
    const h = reg.registerTarget(node, 'impact:I1', 't1');
    expect(reg.entityOfElement(node, 't1')).toEqual({ type: 'impact', id: 'I1' });
    const release = reg.highlightTarget('impact:I1', 't1');
    release();
    expect(calls).toEqual(['scroll', 'add', 'remove']);
    expect(reg.highlightTarget('impact:none', 't1')).toBeTypeOf('function');
    h.destroy();
    expect(reg.targetElement('impact:I1', 't1')).toBeUndefined();
  });

  it('shares a field value only when the view opts in', () => {
    const node = el();
    const a = registerField(node, spec('plain', { get: () => 'p' }), 't1');
    const b = registerField(el(), spec('open', { share: true, get: () => 'visible' }), 't1');
    expect(reg.sharedValue(node, 't1')).toBeUndefined();
    expect(reg.sharedValue(fieldOf('open', 't1')?.el, 't1')).toBe('visible');
    a.destroy();
    b.destroy();
  });
});
