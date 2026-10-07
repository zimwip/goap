import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const cur = vi.hoisted(() => ({ id: 'change:C1' }));
vi.mock('../shell/tabs.svelte', () => ({ activeTab: () => ({ id: cur.id }) }));

import { assistantContext, clipBytes, MAX_CONTEXT_BYTES, MAX_ENTITIES, MAX_ERRORS } from './context';
import { registerAssist, registerField, resetRegistry, registerTarget } from '../assist/registry.svelte';
import { forgetSelection, recordFocus, recordSelection, resetCapture } from '../assist/capture';
import { recordAction, resetRecorder } from '../assist/recorder';

const tab = { id: 'change:C1', kind: 'change', params: { id: 'C1' }, pinned: true };
const el = () => ({ isConnected: true, contains: () => false }) as unknown as HTMLElement;
const NOW = 1_000_000;

beforeEach(() => {
  resetRegistry();
  resetCapture();
  resetRecorder();
  cur.id = tab.id;
});
afterEach(() => vi.unstubAllGlobals());

describe('assistant context layers', () => {
  it('has the app layer alone for a screen with no view registered', () => {
    expect(assistantContext(tab, 'PROJ-A', NOW)).toEqual({ app: { tab: { kind: 'change', params: { id: 'C1' } }, project: 'PROJ-A' }, screen: { kind: 'change' } });
    expect(assistantContext(undefined, '', NOW)).toEqual({});
  });

  it('adds the screen and the focus the views declare', () => {
    const off = registerAssist({
      screen: () => ({ kind: 'change', title: 'Fix login', summary: 'Change, active.', entities: [{ type: 'impact', id: 'I1', label: 'REQ-1', state: 'proposed', props: { type: 'alm@Requirement' } }] }),
      focus: () => ({ dialogKind: 'edit', dialogTitle: 'Edit', pendingAction: 'editing the title', errors: ['The title is required.'] }),
    });
    recordAction('accepted the impact of REQ-0', NOW - 1000);
    const c = assistantContext(tab, 'PROJ-A', NOW);
    expect(c.screen).toEqual({ kind: 'change', title: 'Fix login', summary: 'Change, active.', entities: [{ type: 'impact', id: 'I1', label: 'REQ-1', state: 'proposed', props: { type: 'alm@Requirement' } }] });
    expect(c.focus).toEqual({ dialogKind: 'edit', dialogTitle: 'Edit', pendingAction: 'editing the title', errors: ['The title is required.'], lastAction: 'accepted the impact of REQ-0' });
    off();
  });

  it('zooms: the focused entity comes first, whether the view or the page focus names it', () => {
    const ents = ['I1', 'I2', 'I3', 'I4'].map((id) => ({ type: 'impact', id, label: id }));
    const off = registerAssist({ screen: () => ({ entities: ents }), focus: () => ({ element: { type: 'impact', id: 'I3', label: 'I3' } }) });
    expect(assistantContext(tab, '', NOW).screen?.entities?.map((e) => e.id)).toEqual(['I3', 'I1', 'I2', 'I4']);
    off();

    // the element the person had focused before going to the assistant
    const node = el();
    const t = registerTarget(node, 'impact:I4', tab.id);
    const off2 = registerAssist({ screen: () => ({ entities: ents }) });
    recordFocus(node, NOW - 5000);
    const c = assistantContext(tab, '', NOW);
    expect(c.focus?.element).toEqual({ type: 'impact', id: 'I4' });
    expect(c.screen?.entities?.[0].id).toBe('I4');
    // a focus too old is not used
    expect(assistantContext(tab, '', NOW + 10 * 60 * 1000).focus?.element).toBeUndefined();
    off2();
    t.destroy();
  });

  it('caps entities, errors, props and texts as the server does', () => {
    const many = Array.from({ length: 100 }, (_, i) => ({ type: 'impact', id: `I${i}`, label: `L${i}`, props: Object.fromEntries(Array.from({ length: 10 }, (_, k) => [`k${k}`, 'v'.repeat(200)])) }));
    const off = registerAssist({ screen: () => ({ title: 'é'.repeat(300), summary: 's'.repeat(900), entities: many }), focus: () => ({ errors: Array.from({ length: 20 }, (_, i) => `e${i}${'x'.repeat(300)}`), pendingAction: 'p'.repeat(500) }) });
    const c = assistantContext(tab, '', NOW);
    expect(c.screen!.entities!.length).toBeLessThanOrEqual(MAX_ENTITIES);
    expect(Object.keys(c.screen!.entities![0].props!).length).toBe(6);
    expect(new TextEncoder().encode(c.screen!.entities![0].props!.k0).length).toBeLessThanOrEqual(80);
    expect(c.focus!.errors!.length).toBe(MAX_ERRORS);
    expect(c.focus!.errors!.every((e) => new TextEncoder().encode(e).length <= 200)).toBe(true);
    expect(new TextEncoder().encode(c.screen!.title!).length).toBeLessThanOrEqual(200);
    expect(c.screen!.summary!.length).toBe(600);
    expect(new TextEncoder().encode(c.focus!.pendingAction!).length).toBeLessThanOrEqual(300);
    expect(new TextEncoder().encode(JSON.stringify(c)).length).toBeLessThanOrEqual(MAX_CONTEXT_BYTES);
    off();
  });

  it('drops entities from the end, never the focused one, when the context is too big for the byte budget', () => {
    const fat = Array.from({ length: 40 }, (_, i) => ({ type: 'impact', id: `I${i}`, label: 'l'.repeat(200), state: 's'.repeat(60), props: Object.fromEntries(Array.from({ length: 6 }, (_, k) => [`k${k}`, 'v'.repeat(80)])) }));
    const off = registerAssist({ screen: () => ({ summary: 'x'.repeat(600), entities: fat }), focus: () => ({ element: { type: 'impact', id: 'I30' } }) });
    const c = assistantContext(tab, 'P', NOW);
    expect(new TextEncoder().encode(JSON.stringify(c)).length).toBeLessThanOrEqual(MAX_CONTEXT_BYTES);
    expect(c.screen!.entities!.length).toBeLessThan(40);
    expect(c.screen!.entities![0].id).toBe('I30');
    off();
  });

  it('deduplicates entities', () => {
    const off = registerAssist({ screen: () => ({ entities: [{ type: 'impact', id: 'I1' }, { type: 'impact', id: 'I1' }] }) });
    expect(assistantContext(tab, '', NOW).screen?.entities).toHaveLength(1);
    off();
  });
});

describe('selection capture', () => {
  it('uses the last non-empty selection captured within the window, even if cleared at send time', () => {
    recordSelection('chosen text', NOW - 20_000);
    recordSelection('', NOW - 1000); // clicking into the input clears the page selection
    expect(assistantContext(tab, '', NOW).focus?.selection).toBe('chosen text');
  });

  it('forgets a selection that is too old, and one already used', () => {
    recordSelection('old', NOW - 5 * 60_000);
    expect(assistantContext(tab, '', NOW).focus).toBeUndefined();
    recordSelection('fresh', NOW - 1000);
    forgetSelection();
    expect(assistantContext(tab, '', NOW).focus).toBeUndefined();
  });

  it('caps the selection at 2000 bytes', () => {
    recordSelection('é'.repeat(3000), NOW);
    expect(new TextEncoder().encode(assistantContext(tab, '', NOW).focus!.selection!).length).toBeLessThanOrEqual(2000);
    expect(clipBytes('abc', 2)).toBe('ab');
  });
});

describe('what never leaves the page', () => {
  it('does not send the value of a field, only its name, label and guidance', () => {
    const node = el();
    const h = registerField(node, { id: 'title', label: 'Title', type: 'string', get: () => 'CONFIDENTIAL', set: () => {} }, tab.id);
    recordFocus(node, NOW);
    const c = assistantContext(tab, '', NOW);
    expect(c.focus?.element).toEqual({ type: 'field', id: 'title', label: 'Title' });
    expect(JSON.stringify(c)).not.toContain('CONFIDENTIAL');
    h.destroy();
  });

  it('sends the value of the focused field only when its view opts in, cut to 80 bytes', () => {
    const node = el();
    const h = registerField(node, { id: 'title', label: 'Title', type: 'string', share: true, get: () => 'v'.repeat(200), set: () => {} }, tab.id);
    recordFocus(node, NOW);
    const entity = assistantContext(tab, '', NOW).screen!.entities!.find((e) => e.id === 'title')!;
    expect(entity.props!.value).toHaveLength(80);
    h.destroy();
  });

  it('keeps other tabs out', () => {
    const off = registerAssist({ tab: 'node:N', screen: () => ({ kind: 'node', title: 'secret node' }) });
    expect(JSON.stringify(assistantContext(tab, '', NOW))).not.toContain('secret node');
    off();
  });
});
