import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const tab = vi.hoisted(() => ({ id: 'node:A' }));
const suggest = vi.hoisted(() => vi.fn());
const enabled = vi.hoisted(() => ({ on: true }));
vi.mock('../shell/tabs.svelte', () => ({ activeTab: () => ({ id: tab.id, kind: 'node', params: { key: 'REQ-1' }, pinned: true }) }));
vi.mock('../api', () => ({ models: { suggest } }));
vi.mock('./enabled', () => ({ helperEnabled: () => enabled.on }));

import { registerField } from './fields.svelte';
import { accept, canOpenHelper, closeHelper, comment, currentProposal, helper, openHelper } from './helper.svelte';

const el = () => ({ isConnected: true }) as unknown as HTMLElement;
let title = 'Old';
const set = vi.fn((v: unknown) => (title = String(v)));
const handles: { destroy: () => void }[] = [];

beforeEach(() => {
  title = 'Old';
  enabled.on = true;
  handles.push(
    registerField(el(), { id: 'title', label: 'Title', type: 'string', get: () => title, set }, tab.id),
    registerField(el(), { id: 'status', label: 'Status', type: 'enum', enum: ['open', 'closed'], get: () => '', set }, tab.id),
    registerField(el(), { id: 'key', label: 'Key', readOnly: true, get: () => 'K', set }, tab.id),
  );
});

afterEach(() => {
  closeHelper();
  while (handles.length) handles.pop()?.destroy();
  suggest.mockReset();
  set.mockClear();
});

const answer = {
  message: 'Here',
  proposals: [
    { fieldId: 'title', value: '"New title"', rationale: 'clearer' },
    { fieldId: 'status', value: '"closed"' },
    { fieldId: 'key', value: '"Z"' },
    { fieldId: 'ghost', value: '1' },
    { fieldId: 'title', value: 'not json' },
  ],
};

describe('helper store', () => {
  it('opens only when the alias is available and the tab has fields', () => {
    expect(canOpenHelper()).toBe(true);
    enabled.on = false;
    expect(canOpenHelper()).toBe(false);
  });

  it('asks with the context of the tab and keeps the proposals of known, writable fields', async () => {
    suggest.mockResolvedValue(answer);
    await openHelper();
    const req = suggest.mock.calls[0][0];
    expect(req.context.fields.map((f: { id: string }) => f.id)).toEqual(['title', 'status', 'key']);
    expect(req.messages).toEqual([]);
    expect(helper.open).toBe(true);
    expect(helper.loading).toBe(false);
    expect(helper.proposals.map((p) => p.fieldId)).toEqual(['title', 'status']);
    expect(currentProposal()).toMatchObject({ value: 'New title', rationale: 'clearer' });
    expect(helper.messages).toEqual([{ role: 'assistant', text: 'Here' }]);
  });

  it('accepts through the setter of the field, goes to the next proposal, then closes', async () => {
    suggest.mockResolvedValue(answer);
    await openHelper();
    accept();
    expect(set).toHaveBeenCalledWith('New title');
    expect(title).toBe('New title');
    expect(helper.open).toBe(true);
    expect(currentProposal()?.fieldId).toBe('status');
    accept();
    expect(set).toHaveBeenLastCalledWith('closed');
    expect(helper.open).toBe(false);
    expect(helper.messages).toEqual([]);
  });

  it('resends the context and the discussion on a comment and replaces the proposals', async () => {
    suggest.mockResolvedValueOnce(answer).mockResolvedValueOnce({ message: 'Shorter', proposals: [{ fieldId: 'title', value: '"Short"' }] });
    await openHelper();
    await comment('  make it shorter ');
    const req = suggest.mock.calls[1][0];
    expect(req.messages).toEqual([
      { role: 'assistant', text: 'Here' },
      { role: 'user', text: 'make it shorter' },
    ]);
    expect(helper.proposals.map((p) => p.value)).toEqual(['Short']);
    expect(helper.index).toBe(0);
  });

  it('discards everything on reject', async () => {
    suggest.mockResolvedValue(answer);
    await openHelper();
    closeHelper();
    expect(helper).toMatchObject({ open: false, loading: false, error: '', messages: [], proposals: [], index: 0, note: '' });
  });

  it('shows an error of the server and ignores an answer that arrives after the close', async () => {
    suggest.mockRejectedValueOnce(new Error('the helper alias is not available'));
    await openHelper();
    expect(helper.error).toMatch(/not available/);
    closeHelper();
    let resolve!: (v: unknown) => void;
    suggest.mockReturnValueOnce(new Promise((r) => (resolve = r)));
    const p = openHelper();
    closeHelper();
    resolve(answer);
    await p;
    expect(helper.proposals).toEqual([]);
    expect(helper.open).toBe(false);
  });

  it('reports a field that left the page and keeps the proposal', async () => {
    suggest.mockResolvedValue(answer);
    await openHelper();
    handles[0].destroy();
    accept();
    expect(helper.error).toMatch(/no longer/);
    expect(helper.open).toBe(true);
  });
});
