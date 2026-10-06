import { describe, expect, it } from 'vitest';
import type { LogEntry } from './api';
import { familyEntries, familyKeys } from './subChangeHistory';

let at = 0;
const log = (change: string, seq: number, e: Record<string, unknown>): LogEntry => ({
  seq: String(seq),
  type: `impact.${e.op}`,
  payload: JSON.stringify({
    changeId: change,
    at: `2026-01-01T00:00:${String(at++).padStart(2, '0')}Z`,
    ...e,
  }),
});

// ADR 0081, 0082: a parent drafts a node, a sub-change copies the draft, the parent moves on, the sub-change is rebased and
// integrated into the parent.
describe('familyEntries', () => {
  at = 0;
  const parent = [
    log('P', 1, {
      op: 'proposed',
      impactId: 'p1',
      state: { id: 'p1', key: 'REQ-1' },
    }),
    log('P', 2, {
      op: 'checkedOut',
      impactId: 'p1',
      post: { id: 'n1', version: 0 },
      draft: {},
    }),
  ];
  const sub = [
    log('S', 1, {
      op: 'checkedOut',
      impactId: 's1',
      state: { id: 's1', key: 'REQ-1' },
      draft: { inherited: { changeId: 'P', impactId: 'p1', seq: 2 } },
    }),
  ];
  parent.push(
    log('P', 3, {
      op: 'updated',
      impactId: 'p1',
      patch: { props: { title: 'p' } },
    }),
  );
  sub.push(
    log('S', 2, {
      op: 'transitioned',
      impactId: 's1',
      draft: {},
      patch: { rebased: { change: 'P', seq: 3 }, conflicts: ['props.title'] },
    }),
  );
  sub.push(
    log('S', 3, {
      op: 'reviewed',
      impactId: 's1',
      review: { status: 'accepted', comment: 'ok' },
    }),
  );
  parent.push(
    log('P', 4, {
      op: 'transitioned',
      impactId: 'p1',
      draft: {},
      patch: { integrated: { change: 'S', impact: 's1' } },
    }),
  );
  sub.push(
    log('S', 4, {
      op: 'integrated',
      impactId: 's1',
      into: { changeId: 'P', impactId: 'p1' },
    }),
  );
  parent.push(
    log('P', 5, {
      op: 'landed',
      impactId: 'p1',
      landed: { id: 'n1', version: 2 },
      branch: 'main',
    }),
  );
  // another node, filtered out by the key
  parent.push(
    log('P', 6, {
      op: 'proposed',
      impactId: 'p2',
      state: { id: 'p2', key: 'REQ-2' },
    }),
  );

  const members = [
    { id: 'P', title: 'parent' },
    { id: 'S', title: 'sub', parentId: 'P' },
  ];
  const logs = new Map([
    ['P', parent],
    ['S', sub],
  ]);
  const entries = familyEntries(members, logs, 'REQ-1');
  const byId = new Map(entries.map((e) => [e.id, e]));

  it('keeps one node, newest first, without the hand-over row of the sub-change', () => {
    expect(entries.map((e) => e.id)).toEqual(['P:5', 'P:4', 'S:3', 'S:2', 'P:3', 'S:1', 'P:2', 'P:1']);
    expect(familyKeys(familyEntries(members, logs))).toEqual(['REQ-1', 'REQ-2']);
  });

  it('ties the lanes: the fork, the rebase, the integration', () => {
    expect(byId.get('S:1')?.parents.map((p) => p.id)).toEqual(['P:2']);
    expect(byId.get('S:1')?.item.kind).toBe('fork');
    // a lane that copies a draft starts at that copy, with no fallback edge
    expect(byId.get('S:2')?.parents.map((p) => p.id)).toEqual(['S:1', 'P:3']);
    expect(byId.get('S:2')?.item.label).toBe('rebased onto the parent — conflicts: props.title');
    expect(byId.get('P:4')?.parents.map((p) => p.id)).toEqual(['P:3', 'S:3']);
    expect(byId.get('P:4')?.item.label).toBe('integrated from sub-change sub');
    expect(byId.get('P:5')?.item.label).toBe('landed as v2 on main');
  });
});
