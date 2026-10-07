import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../shell/tabs.svelte', () => ({ activeTab: () => undefined }));

import { collectContext, MAX_FIELDS, subjectOf } from './context';
import { registerField, type FieldSpec } from '../assist/registry.svelte';

const el = () => ({ isConnected: true }) as unknown as HTMLElement;
const tab = { id: 'node:A', kind: 'node', params: { key: 'REQ-1', ns: 'alm' }, pinned: true };
const handles: { destroy: () => void }[] = [];
const add = (spec: Partial<FieldSpec> & { id: string }) => handles.push(registerField(el(), { label: spec.id, get: () => '', set: () => {}, ...spec }, tab.id));

afterEach(() => {
  while (handles.length) handles.pop()?.destroy();
  vi.unstubAllGlobals();
});

describe('collectContext', () => {
  it('describes the tab, its subject, the selection and the fields', () => {
    vi.stubGlobal('window', { getSelection: () => ({ toString: () => '  some text  ' }) });
    add({ id: 'title', label: 'Title', type: 'string', get: () => 'Hello' });
    add({ id: 'size', type: 'number', get: () => 12 });
    add({ id: 'status', type: 'enum', enum: ['open', 'closed'], get: () => '', description: 'where it stands' });
    add({ id: 'key', readOnly: true, get: () => 'REQ-1' });
    const c = collectContext(tab);
    expect(c.tab).toEqual({ kind: 'node', params: { key: 'REQ-1', ns: 'alm' } });
    expect(c.subject).toBe('REQ-1');
    expect(c.selection).toBe('some text');
    expect(c.fields).toEqual([
      { id: 'title', label: 'Title', type: 'string', currentValue: '"Hello"' },
      { id: 'size', label: 'size', type: 'number', currentValue: '12' },
      { id: 'status', label: 'status', type: 'enum', enumValues: ['open', 'closed'], description: 'where it stands' },
      { id: 'key', label: 'key', readOnly: true, currentValue: '"REQ-1"' },
    ]);
  });

  it('keeps only the fields of its tab, caps their number and the size of their values', () => {
    vi.stubGlobal('window', {});
    handles.push(registerField(el(), { id: 'other', label: 'other', get: () => '', set: () => {} }, 'node:B'));
    for (let i = 0; i < MAX_FIELDS + 5; i++) add({ id: `f${i}`, get: () => 'x'.repeat(5000) });
    const c = collectContext(tab);
    expect(c.fields).toHaveLength(MAX_FIELDS);
    expect(c.fields.some((f) => f.id === 'other')).toBe(false);
    expect(c.fields.reduce((n, f) => n + (f.currentValue?.length ?? 0), 0)).toBeLessThan(20 * 1024);
    expect(c.selection).toBe('');
  });

  it('has no subject without a naming param, and an empty context without a tab', () => {
    expect(subjectOf({ params: { pane: 'x' } })).toBe('');
    expect(collectContext(undefined).fields).toEqual([]);
  });
});
