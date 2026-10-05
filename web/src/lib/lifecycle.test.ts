import { beforeEach, describe, expect, it, vi } from 'vitest';
import { acceptAllProposed, awaitingReview, holdsOwnDraft, lifecycleRows, loadPosts, postKey, writeNodeInChange } from './lifecycle';
import { graph, isDraft, type ChangeImpact, type Lifecycle } from './api';
import type { TypeCatalog } from './stores/types.svelte';

// A state is landable unless flagged notLandable (ADR 0078): the row of a node says whether it can land as it stands.
const lc: Lifecycle = {
  initial: 'draft',
  states: [{ name: 'draft', notLandable: true }, { name: 'approved' }],
  transitions: [{ name: 'approve', from: 'draft', to: 'approved' }],
};
const cat = { lifecycle: (t?: string) => (t === 'Req' ? lc : undefined), properties: () => [], open: (t?: string) => t === 'Free' } as unknown as TypeCatalog;

describe('lifecycleRows', () => {
  it('marks a node in a notLandable state, and only that one', () => {
    const nodes = [
      { id: 'a', key: 'REQ-A', type: 'Req', version: 1, state: 'draft' },
      { id: 'b', key: 'REQ-B', type: 'Req', version: 1, state: 'approved' },
      { id: 'c', key: 'NOTE-C', type: 'Note', version: 1, state: '' },
    ];
    const rows = lifecycleRows(cat, nodes, nodes.map((n) => ({ id: n.id })), [], new Map(), []);
    const landable = Object.fromEntries(rows.map((r) => [r.node.key, r.landable]));
    expect(landable).toEqual({ 'REQ-A': false, 'REQ-B': true, 'NOTE-C': true });
  });

  // Strict attributes: a row tells whether its type is free-form, the only case where a property that is no attribute may be set.
  it('says whether the type is free-form', () => {
    const nodes = [
      { id: 'a', key: 'REQ-A', type: 'Req', version: 1 },
      { id: 'f', key: 'FREE-F', type: 'Free', version: 1 },
    ];
    const rows = lifecycleRows(cat, nodes, nodes.map((n) => ({ id: n.id })), [], new Map(), []);
    expect(Object.fromEntries(rows.map((r) => [r.node.key, r.open]))).toEqual({ 'REQ-A': false, 'FREE-F': true });
  });
});

// ADR 0079: a node has no version while a change works on it; its impact's post is a draft reference.
describe('isDraft', () => {
  it('is an id with no version', () => {
    expect(isDraft({ id: 'n1', version: 0 })).toBe(true);
    expect(isDraft({ id: 'n1' })).toBe(true);
    expect(isDraft({ id: 'n1', version: 3 })).toBe(false);
    expect(isDraft({ version: 0 })).toBe(false);
    expect(isDraft(undefined)).toBe(false);
  });
});

describe('the edit flow never reviews (ADR 0079)', () => {
  const calls: string[] = [];
  const rec = (name: string, ret: unknown = {}) =>
    vi.fn(async (...a: unknown[]) => {
      calls.push(`${name}:${JSON.stringify(a.slice(1, 3))}`);
      return ret;
    });
  const impact: ChangeImpact = { id: 'i1', key: 'REQ-A', intent: 'modified', pre: { id: 'n1', version: 2 }, review: 'proposed' };
  const drafted: ChangeImpact = { ...impact, post: { id: 'n1', version: 0 } };

  beforeEach(() => {
    calls.length = 0;
    Object.assign(graph, {
      impactNodeCreate: rec('create', { node: { ...impact, intent: 'created', post: { id: 'n9', version: 0 } } }),
      impactNodeCheckout: rec('checkout', { node: drafted }),
      impactNodeUpdate: rec('update', { node: drafted }),
      impactLinkCreate: rec('linkCreate'),
      impactLinkDelete: rec('linkDelete'),
      impactNodeTransition: rec('transition', { node: { ...drafted, review: 'accepted' } }),
      impactNodeReview: rec('review', { node: drafted }),
      getBlackboard: rec('blackboard', { change: { nodes: [drafted] } }),
      getNode: vi.fn(async () => ({ view: { node: { id: 'n1', draft: true }, out: [{ id: 'l1', type: 'refines', to: { id: 'n2' } }] } })),
    });
  });

  it('checks out a node without draft, then updates it, and calls no review', async () => {
    await writeNodeInChange('c1', [impact], { pre: impact.pre }, { props: { title: 'x' } }, 'edit', 'main');
    expect(calls.map((c) => c.split(':')[0])).toEqual(['checkout', 'update']);
  });

  it('only updates a node the flow already holds a draft of', async () => {
    await writeNodeInChange('c1', [drafted], { pre: impact.pre }, { props: { title: 'x' }, addLinks: [{ type: 'refines', to: { id: 'n2', version: 0 } }] }, 'edit', 'main');
    expect(calls.map((c) => c.split(':')[0])).toEqual(['update', 'linkCreate']);
  });

  it('checks out again when the draft belongs to a parent flow', async () => {
    await writeNodeInChange('c1', [drafted], { pre: impact.pre }, { props: { title: 'x' } }, 'edit', 'opt-1');
    expect(calls.map((c) => c.split(':')[0])).toEqual(['checkout', 'update']);
  });

  it('falls back to an update when the checkout conflicts', async () => {
    graph.impactNodeCheckout = vi.fn(async () => {
      throw new Error('already checked out');
    }) as never;
    await writeNodeInChange('c1', [drafted], { pre: impact.pre }, { props: { title: 'x' } }, 'edit', 'opt-1');
    expect(calls.map((c) => c.split(':')[0])).toEqual(['update']);
  });

  it('a transition does not review, even when it answers an accepted impact', async () => {
    await writeNodeInChange('c1', [impact], { pre: impact.pre }, { state: 'approved' }, 'move', 'main');
    expect(calls.map((c) => c.split(':')[0])).toEqual(['transition']);
  });

  it('a creation does not review', async () => {
    await writeNodeInChange('c1', [], { key: 'REQ-N', type: 'alm@Requirement' }, { props: {} }, 'create', 'main');
    expect(calls.map((c) => c.split(':')[0])).toEqual(['create']);
  });

  it('deletes a link through the draft view', async () => {
    await writeNodeInChange('c1', [drafted], { pre: impact.pre }, { removeLinks: ['l1'] }, 'unlink', 'main');
    expect(calls.map((c) => c.split(':')[0])).toEqual(['linkDelete']);
  });

  it('reads a draft through the change, never by a version', async () => {
    const posts = await loadPosts([drafted], false, { changeId: 'c1', flow: 'main' });
    expect(graph.getNode).toHaveBeenCalledWith({ id: 'n1', version: 0 }, undefined, { changeId: 'c1', flow: 'main' });
    expect(posts.get(postKey({ id: 'n1', version: 0 }))?.draft).toBe(true);
  });

  it('lists the impacts awaiting a review and accepts them only on explicit request', async () => {
    const accepted: ChangeImpact = { id: 'i2', post: { id: 'n2', version: 0 }, review: 'accepted' };
    const planned: ChangeImpact = { id: 'i3', pre: { id: 'n3', version: 1 }, review: 'proposed' };
    expect(awaitingReview([drafted, accepted, planned]).map((c) => c.id)).toEqual(['i1']);
    await acceptAllProposed('c1', [drafted, accepted, planned], 'all good', 'main');
    expect(calls.map((c) => c.split(':')[0])).toEqual(['review']);
  });

  it('holds its own draft only on the flow that declared the impact', () => {
    expect(holdsOwnDraft(drafted, 'main')).toBe(true);
    expect(holdsOwnDraft(drafted, 'opt-1')).toBe(false);
    expect(holdsOwnDraft({ ...drafted, flow: 'opt-1' }, 'opt-1')).toBe(true);
    expect(holdsOwnDraft(impact, 'main')).toBe(false);
  });
});
