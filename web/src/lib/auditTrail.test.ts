import { describe, expect, it } from 'vitest';
import { buildTrail, reviewGroups } from './auditTrail';
import type { ImpactEvent, LogEntry } from './api';

const entry = (seq: number, e: ImpactEvent): LogEntry => ({ seq: String(seq), type: `impact.${e.op}`, payload: JSON.stringify({ id: `e${seq}`, impactId: 'i1', ...e }) });

// ADR 0079: a user edit audit reads proposed, checkedOut, updated... and no `reviewed` line until a reviewer acts; a
// draft is shown as "draft", never as a version.
describe('buildTrail', () => {
  const draft = { id: 'n1', version: 0 };
  const log = [
    entry(1, { op: 'proposed', state: { key: 'REQ-A', intent: 'modified', pre: { id: 'n1', version: 2 } } }),
    entry(2, { op: 'checkedOut', pre: { id: 'n1', version: 2 }, post: draft }),
    entry(3, { op: 'updated', post: draft, patch: { props: { title: 'x' } } }),
    entry(4, { op: 'transitioned', post: draft, patch: { state: { from: 'draft', to: 'approved' } } }),
    entry(5, { op: 'committed', committed: { id: 'n1', version: 3 }, branch: 'main' }),
  ];
  const trail = buildTrail({ nodes: [{ id: 'i1', key: 'REQ-A' }] }, log, new Map(), false);

  it('has no review line for an edit', () => {
    expect(trail.map((t) => t.label)).toEqual(['proposed', 'checkedOut', 'updated', 'transitioned', 'committed']);
  });

  it('shows a draft, not a version', () => {
    const text = trail.map((t) => t.summary).join('\n');
    expect(text).not.toMatch(/v0/);
    expect(trail[1].summary).toBe('draft of the node, checked out from v2');
    expect(trail[3].summary).toBe('draft moved from draft to approved');
  });

  it('shows the version written at commit', () => {
    expect(trail[4].summary).toBe('draft merged as v3 into main');
  });

  it('says a commit on the change branch is a commit', () => {
    const own = buildTrail({ nodes: [] }, [entry(1, { op: 'committed', committed: { id: 'n1', version: 3 }, branch: 'change-ab12cd34' })], new Map(), false);
    expect(own[0].summary).toBe('draft committed as v3 on branch change-ab12cd34');
  });
});

// ADR 0080: the verdicts of a submitted review carry its id; its deltas are facts of the log, and the verdicts are
// derived from the one submitting it (ADR 0105 addendum).
describe('review objects in the trail', () => {
  const item = (seq: number, data: Record<string, unknown>): LogEntry => ({
    seq: String(seq),
    type: 'fact.review',
    payload: JSON.stringify({ id: `it${seq}`, type: 'review', status: 'proposed', producedBy: 'alice', createdAt: `2026-01-0${seq}T00:00:00Z`, data }),
  });
  const reviewed = (seq: number, impactId: string, status: string): LogEntry => ({
    seq: String(seq),
    type: 'impact.reviewed',
    payload: JSON.stringify({ id: `e${seq}`, impactId, op: 'reviewed', by: 'alice', review: { status, comment: 'c' } }),
  });
  const log = [
    item(1, { key: 'REV-1', status: 'open', comment: 'global' }),
    reviewed(2, 'i1', 'proposed'),
    item(3, { key: 'REV-1', entries: [{ impact: 'i1', outcome: 'accept', comment: 'c' }] }),
    item(4, { key: 'REV-1', entries: [{ impact: 'i2', outcome: 'reject', comment: 'c' }] }),
    item(5, { key: 'REV-1', status: 'submitted' }),
  ];
  const trail = buildTrail({ nodes: [{ id: 'i1', key: 'REQ-A' }, { id: 'i2', key: 'REQ-B' }] }, log, new Map(), false);

  it('shows the review as each delta leaves it', () => {
    expect(trail[0].label).toBe('review open');
    expect(trail[0].subject).toBe('REV-1');
    expect(trail[4].label).toBe('review submitted');
    expect(trail[4].summary).toContain('1 accepted, 1 rejected');
    expect(trail[4].tone).toBe('ok');
  });

  it('derives the verdicts of the submission, each at the time of its outcome', () => {
    expect(trail[1].reviewId).toBeUndefined();
    expect(trail).toHaveLength(7);
    expect(trail[5].summary).toBe('accepted in review REV-1 — c\n\nReview: global');
    expect(trail[5].at).toBe('2026-01-03T00:00:00Z');
    expect(trail[6].tone).toBe('warn');
  });

  it('groups the entries of a review', () => {
    const groups = reviewGroups(trail);
    expect([...groups.keys()]).toEqual(['REV-1']);
    expect(groups.get('REV-1')!.map((e) => e.seq)).toEqual([1, 3, 4, 5, 5, 5]);
  });
});

// ADR 0082: a rebase onto the parent names its conflicts.
describe('a sub-change rebased onto its parent', () => {
  const log = [entry(1, { op: 'transitioned', post: { id: 'n1', version: 0 }, patch: { rebased: { change: 'parent-change-id', seq: 4 }, conflicts: ['props.title'] } })];
  const trail = buildTrail({ nodes: [{ id: 'i1', key: 'REQ-A' }] }, log, new Map(), false);

  it('names the parent and the conflicts', () => {
    expect(trail[0].summary).toBe('draft rebased onto parent change parent-c — conflicts: props.title');
    expect(trail[0].tone).toBe('warn');
  });
});

// ADR 0091: a change that moved to another project says so in its trail.
describe('a move to another project', () => {
  const header = (fields: Record<string, { from?: string; to?: string }>): LogEntry => ({
    id: 'h1',
    seq: '1',
    type: 'change.updated',
    by: 'alice',
    subject: Object.keys(fields).join(','),
    payload: JSON.stringify({ fields }),
  });

  it('reads "project: A → B"', () => {
    const [e] = buildTrail({ nodes: [] }, [header({ projectId: { from: 'PROJ-A', to: 'PROJ-B' } })], new Map(), false);
    expect(e.label).toBe('moved');
    expect(e.subject).toBe('project');
    expect(e.summary).toBe('project: PROJ-A → PROJ-B');
    expect(e.by).toBe('alice');
  });

  it('keeps an ordinary edit an edit', () => {
    const [e] = buildTrail({ nodes: [] }, [header({ title: { from: 'a', to: 'b' } })], new Map(), false);
    expect(e.label).toBe('edited');
  });
});

// ADR 0101: a root change moved onto the head of its target branch.
describe('a root change rebased onto its target', () => {
  const header = (payload: object): LogEntry => ({ id: 'h2', seq: '2', type: 'change.updated', by: 'bob', subject: 'forkCommit', payload: JSON.stringify(payload) });

  it('reads the move of the commit', () => {
    const [e] = buildTrail({ nodes: [] }, [header({ branch: 'main', fields: { forkCommit: { from: 'BBBBBBBB-1', to: 'T1T1T1T1-2' } } })], new Map(), false);
    expect(e.label).toBe('rebased');
    expect(e.summary).toBe('rebased onto main head (T1T1T1T1), was BBBBBBBB');
  });

  it('reads a draft rebased onto a branch', () => {
    const log = [
      entry(1, { op: 'transitioned', post: { id: 'n1', version: 0 }, patch: { rebased: { branch: 'main', commit: 'T1' }, conflicts: ['props.title'] } }),
      entry(2, { op: 'transitioned', post: { id: 'n1', version: 0 }, patch: { rebased: { branch: 'main', commit: 'T1' }, conflicts: [] } }),
    ];
    const trail = buildTrail({ nodes: [{ id: 'i1', key: 'REQ-A' }] }, log, new Map(), false);
    expect(trail[0].summary).toBe('draft rebased onto main (conflicts: props.title)');
    expect(trail[1].summary).toBe('draft rebased onto main (merged)');
  });
});
