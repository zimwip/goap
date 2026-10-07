import { describe, expect, it, vi } from 'vitest';
import { availableTransitions, confirmMessage, consumedDecisions, gateSummary, movable, parseRefusal, pickableDecisions, resolveCall, runTransition, type TransitionDeps } from './changeTransition';
import type { ChangeItem, Lifecycle } from './api';

const lc: Lifecycle = {
  name: 'change',
  initial: 'draft',
  transitions: [
    { name: 'submit', from: 'draft', to: 'review' },
    { name: 'approve', from: 'review', to: 'approved', guard: 'change.decision != ""', vetos: [{ name: 'no_open_risk' }], objectives: [{ name: 'coverage' }] },
    { name: 'back', from: 'review', to: 'draft' },
  ],
};
const approve = availableTransitions(lc, 'review')[0];
const points = [
  { id: 'D1', status: 'decided', question: 'Which option?', option: 'A' },
  { id: 'D2', status: 'open' },
  { id: 'D3', status: 'decided' },
];

describe('transitions of a change lifecycle', () => {
  it('lists those leaving the state, flags the decision and summarises the gate', () => {
    expect(availableTransitions(lc, 'draft').map((t) => t.name)).toEqual(['submit']);
    expect(availableTransitions(lc, 'review').map((t) => t.name)).toEqual(['approve', 'back']);
    expect(approve.needsDecision).toBe(true);
    expect(approve.gate).toBe('guard: change.decision != ""; vetos: no_open_risk; objectives: coverage');
    expect(gateSummary({ name: 'x' })).toBe('');
    expect(availableTransitions(undefined, 'draft')).toEqual([]);
  });

  it('moves only a change with a lifecycle that is not closed', () => {
    expect(movable({ lifecycle: 'change', status: 'active' })).toBe(true);
    expect(movable({ lifecycle: 'change', status: 'draft' })).toBe(true);
    for (const status of ['applied', 'abandoned', 'committed']) expect(movable({ lifecycle: 'change', status })).toBe(false);
    expect(movable({ status: 'active' })).toBe(false);
  });

  it('offers the decided points no transition used yet', () => {
    const items: ChangeItem[] = [{ id: 'T1', kind: 'transition', data: { transition: 'x', decision: 'D3' } }, { id: 'T2', kind: 'fact', data: { decision: 'D1' } }];
    expect([...consumedDecisions(items)]).toEqual(['D3']);
    expect(pickableDecisions(points, items).map((p) => p.id)).toEqual(['D1']);
  });

  it('reads the refusal of the server', () => {
    const v = parseRefusal('failed_precondition: change C cannot take approve: vetoed by a, b: conflict');
    expect(v.vetoed).toEqual(['a', 'b']);
    const o = parseRefusal('change C cannot take approve: objectives not met and not covered by a derogation: coverage, perf: conflict');
    expect(o.unmet).toEqual(['coverage', 'perf']);
    expect(parseRefusal('change C cannot take approve: gate not satisfied (change.decision != ""): conflict').guard).toBe('change.decision != ""');
    expect(parseRefusal('boom')).toEqual({ message: 'boom', vetoed: [], unmet: [], guard: '' });
  });

  it('states from, to, the decision and the freeze in the confirmation', () => {
    const m = confirmMessage(approve, points[0], approve.gate);
    expect(m).toContain('from “review” to “approved”');
    expect(m).toContain('Which option? → A');
    expect(m).toContain('freezes the impacts');
  });

  it('resolves an assistant call against the offers and the pickable points', () => {
    const offers = availableTransitions(lc, 'review');
    const ok = resolveCall(offers, [points[0]], { transition: 'approve', decision: 'D1' });
    expect(ok).toMatchObject({ offer: { name: 'approve' }, decision: { id: 'D1' } });
    expect(resolveCall(offers, [], { transition: 'nope' })).toMatch(/does not leave the current state; offered: approve, back/);
    expect(resolveCall(offers, [points[0]], { transition: 'approve' })).toMatch(/needs a decided decision point/);
    expect(resolveCall(offers, [points[0]], { transition: 'back', decision: 'D9' })).toMatch(/not decided or was already used/);
    expect(resolveCall(offers, [], { transition: 'back' })).toMatchObject({ offer: { name: 'back' } });
  });
});

describe('runTransition', () => {
  const deps = (over: Partial<TransitionDeps> = {}) => ({
    confirm: vi.fn(async () => true),
    call: vi.fn(async () => ({ change: { state: 'approved' } })),
    refresh: vi.fn(async () => {}),
    notify: vi.fn(),
    ...over,
  });
  const change = { id: 'C1', lifecycle: 'change', status: 'active', state: 'review' };

  it('confirms, calls, refreshes and reports the new state', async () => {
    const d = deps();
    const r = await runTransition(d, { change, offer: approve, decision: { id: 'D1' } });
    expect(r).toEqual({ ok: true, state: 'approved' });
    expect(d.confirm).toHaveBeenCalledOnce();
    expect(d.call).toHaveBeenCalledWith('C1', 'approve', 'D1');
    expect(d.refresh).toHaveBeenCalledOnce();
    expect(d.notify).toHaveBeenCalledWith('Change moved to “approved”.', 'ok');
  });

  it('skips the dialog for the assistant card', async () => {
    const d = deps();
    expect((await runTransition(d, { change, offer: approve, decision: { id: 'D1' }, skipConfirm: true })).ok).toBe(true);
    expect(d.confirm).not.toHaveBeenCalled();
  });

  it('does nothing when the person cancels', async () => {
    const d = deps({ confirm: vi.fn(async () => false) });
    expect(await runTransition(d, { change, offer: approve, decision: { id: 'D1' } })).toMatchObject({ ok: false, cancelled: true });
    expect(d.call).not.toHaveBeenCalled();
  });

  it('refuses before calling when a decision is missing or the change is closed', async () => {
    const d = deps();
    expect(await runTransition(d, { change, offer: approve })).toMatchObject({ ok: false });
    expect(await runTransition(d, { change: { ...change, status: 'applied' }, offer: approve, decision: { id: 'D1' } })).toMatchObject({ ok: false });
    expect(d.call).not.toHaveBeenCalled();
  });

  it('shows the refusal of the server and does not refresh', async () => {
    const d = deps({ call: vi.fn(async () => { throw new Error('cannot take approve: vetoed by no_open_risk: conflict'); }) });
    const r = await runTransition(d, { change, offer: approve, decision: { id: 'D1' }, skipConfirm: true });
    expect(r).toMatchObject({ ok: false, refusal: { vetoed: ['no_open_risk'] } });
    expect(d.refresh).not.toHaveBeenCalled();
  });
});
