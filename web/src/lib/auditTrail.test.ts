import { describe, expect, it } from 'vitest';
import { buildTrail } from './auditTrail';
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
    entry(5, { op: 'landed', landed: { id: 'n1', version: 3 }, branch: 'main' }),
  ];
  const trail = buildTrail({ nodes: [{ id: 'i1', key: 'REQ-A' }] }, log, new Map(), false);

  it('has no review line for an edit', () => {
    expect(trail.map((t) => t.label)).toEqual(['proposed', 'checkedOut', 'updated', 'transitioned', 'landed']);
  });

  it('shows a draft, not a version', () => {
    const text = trail.map((t) => t.summary).join('\n');
    expect(text).not.toMatch(/v0/);
    expect(trail[1].summary).toBe('draft of the node, checked out from v2');
    expect(trail[3].summary).toBe('draft moved from draft to approved');
  });

  it('shows the version written at landing', () => {
    expect(trail[4].summary).toBe('draft landed as v3 on branch main');
  });
});
