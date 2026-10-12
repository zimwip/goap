import { describe, expect, it } from 'vitest';
import { contentFacts, importSummary } from './importScreen';

describe('import screen facts', () => {
  it('describes a graph snapshot without sending it', () => {
    const doc = JSON.stringify({ format: 'goap.graph.snapshot/v1', namespace: 'alm', nodes: [{ key: 'REQ-1', type: 'alm@Requirement', props: { secret: 'x' } }] });
    const f = contentFacts('graph', doc);
    expect(f).toMatch(/^a graph snapshot of namespace alm, 1 node \(\d+ bytes\)$/);
    expect(f).not.toContain('secret');
    expect(contentFacts('graph', '{"nodes": []}')).toMatch(/not a graph snapshot/);
    expect(contentFacts('graph', 'nope')).toMatch(/not valid JSON/);
    expect(contentFacts('graph', '  ')).toMatch(/no content loaded/);
  });

  it('names a definition in YAML or JSON', () => {
    expect(contentFacts('domain', 'name: alm\nversion: 1')).toMatch(/^a domain definition named alm/);
    expect(contentFacts('methodology', '{"name": "sdlc"}')).toMatch(/^a methodology definition named sdlc/);
    expect(contentFacts('methodology', 'goals: []')).toMatch(/no name found/);
  });

  it('summarises the tab and its last outcome', () => {
    expect(importSummary('domain', '', { publish: true }, { definition: { name: 'alm', version: '2', status: 'published', issues: 0 } })).toBe(
      'Import of a domain (YAML or JSON); no content loaded yet (the person picks a file or pastes it); published directly; last import: alm v2 published, 0 issues.',
    );
    expect(importSummary('graph', '', {}, { graph: { unchanged: 3 } })).toContain('nothing differed (3 unchanged), no change created');
  });
});
