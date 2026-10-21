import { describe, expect, it } from 'vitest';
import { bySource, overlaySources, ownSources, projectParents, sourceOf, sourceProblem, sourceProps, sourceRole, sourcesInForce } from './sources';

describe('sources', () => {
  it('reads a declared source from its node', () => {
    expect(sourceOf('SRC:acme', { kind: 'graph', location: 'https://acme', params: { a: 1 } })).toMatchObject({ name: 'acme', kind: 'graph', location: 'https://acme', params: '{"a":1}' });
    expect(sourceOf('LLB:x', {})).toBeUndefined();
    expect(sourceOf('SRC:e', { params: {} })?.params).toBe('');
    expect(sourceOf('SRC:content', { kind: 'graph', location: 'http://c', namespaces: ['alm', 'code'] })?.namespaces).toBe('alm, code');
  });

  it('checks a source like lnk.Source.Validate', () => {
    const s = { name: 'docs', description: '', kind: 'files', namespace: '', location: '', namespaces: '', branch: '', params: '' };
    expect(sourceProblem(s)).toMatch(/root directory/);
    expect(sourceProblem({ ...s, location: '/srv' })).toBe('');
    expect(sourceProblem({ ...s, name: 'Docs', location: '/srv' })).toMatch(/name/);
    expect(sourceProblem({ ...s, location: '/srv', params: '[1]' })).toMatch(/object/);
    expect(sourceProblem({ ...s, location: '/srv', namespaces: 'alm' })).toMatch(/graph at a location/);
    expect(sourceProblem({ ...s, kind: 'graph', location: 'http://c', namespaces: 'alm, code' })).toBe('');
    expect(sourceProps({ ...s, location: '/srv' })).toMatchObject({ name: 'docs', kind: 'files', location: '/srv', namespaces: [], params: {} });
    expect(sourceProps({ ...s, kind: 'graph', location: 'http://c', namespaces: ' alm, ,code' }).namespaces).toEqual(['alm', 'code']);
  });

  it('lists the platform source first and says what each source is', () => {
    const nodes = [
      { key: 'SRC:content', type: 'platform@Source', props: { kind: 'graph', location: 'http://c', namespaces: ['alm'] } },
      { key: 'SRC:platform', type: 'platform@Source', props: { kind: 'graph' } },
    ];
    const rows = overlaySources(nodes as never, 'platform');
    expect(rows.map((r) => r.name)).toEqual(['platform', 'content']);
    expect(rows.map(sourceRole)).toEqual(['this graph (the platform)', 'graph holding alm']);
  });

  it('groups the namespaces of the statistics by source, the platform first', () => {
    const groups = bySource([{ namespace: 'alm', source: 'content' }, { namespace: 'organisation', source: 'platform' }, { namespace: 'x' }]);
    expect(groups.map((g) => [g.source, g.items.map((i) => i.namespace)])).toEqual([
      ['platform', ['organisation', 'x']],
      ['content', ['alm']],
    ]);
  });

  it('resolves the mapping in force along the project chain, the nearest winning', () => {
    const props: Record<string, Record<string, unknown>> = { ROOT: { sources: { alm: 'a', code: 'c' } }, P: { sources: { alm: 'b' } }, Q: {} };
    const parent: Record<string, string> = { P: 'ROOT', Q: 'P' };
    expect(sourcesInForce('Q', (k) => props[k], (k) => parent[k] ?? '')).toEqual([
      { name: 'alm', source: 'b', from: 'P' },
      { name: 'code', source: 'c', from: 'ROOT' },
    ]);
    expect(ownSources({ sources: { x: 'y', bad: 3 } })).toEqual({ x: 'y' });
  });

  it('reads the parents of the projects from their links, a root linking itself', () => {
    const nodes = [
      { id: '1', key: 'ROOT' },
      { id: '2', key: 'P' },
    ];
    const links = [
      { type: 'pp', from: { id: '1' }, to: { id: '1' } },
      { type: 'pp', from: { id: '2' }, to: { id: '1' } },
    ];
    expect([...projectParents(nodes as never, links as never, 'pp')]).toEqual([['P', 'ROOT']]);
  });
});
