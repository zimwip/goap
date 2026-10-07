import { beforeEach, describe, expect, it, vi } from 'vitest';

const tab = vi.hoisted(() => ({ id: 't1' }));
vi.mock('../shell/tabs.svelte', () => ({ activeTab: () => ({ id: tab.id }) }));

import { assistField, fieldOf, fieldsOfTab, hasFields, registerField, type FieldSpec } from './fields.svelte';

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
