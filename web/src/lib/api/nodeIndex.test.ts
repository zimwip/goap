import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { nodeIndex, type NodeSearchRequest } from '../api';

// the index client: one POST per call, the proto3 JSON body of goap.index.v1.SearchRequest

let fetchMock: ReturnType<typeof vi.fn>;

function reply(body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
}

beforeEach(() => {
  const m = new Map<string, string>();
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
  });
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function call(i = 0) {
  const [url, init] = fetchMock.mock.calls[i];
  return { url: url as string, body: JSON.parse(init.body as string) };
}

describe('nodeIndex', () => {
  it('sends the typed search request as it is', async () => {
    const req: NodeSearchRequest = {
      text: 'password reset',
      kinds: ['node', 'change'],
      types: ['alm@Requirement'],
      projects: ['PROJ-A'],
      includeSubprojects: true,
      ownerUnits: ['ORG-A'],
      statuses: ['active'],
      methodologies: ['sdlc'],
      rootsOnly: true,
      mode: 'semantic',
      minSimilarity: 0.5,
      snippet: true,
      limit: 10,
      offset: 20,
      facets: ['kind', 'project'],
    };
    fetchMock.mockReturnValueOnce(
      reply({
        hits: [
          { kind: 'change', id: 'CHG-1', version: 0, key: 'CHG-1', similarity: 0.8, snippet: 'title: Reset', change: { changeId: 'CHG-1', title: 'Reset', status: 'active', projectId: 'PROJ-A' } },
        ],
        total: 1,
        semantic: true,
      }),
    );
    const r = await nodeIndex.search(req);
    expect(call()).toEqual({ url: '/goap.index.v1.IndexService/Search', body: req });
    expect(r.hits?.[0].change?.changeId).toBe('CHG-1');
    expect(r.semantic).toBe(true);
  });

  it('asks for the changes nearest to a change', async () => {
    fetchMock.mockReturnValueOnce(reply({ hits: [], total: 0, semantic: true }));
    await nodeIndex.similarChanges('CHG-7', { projects: ['PROJ-A'], limit: 5, minSimilarity: 0.4 });
    expect(call().body).toEqual({ projects: ['PROJ-A'], limit: 5, minSimilarity: 0.4, kinds: ['change'], similarTo: { kind: 'change', id: 'CHG-7' } });
  });

  it('reads the status of the index', async () => {
    fetchMock.mockReturnValueOnce(reply({ semantic: true, store: '*index.Memory', nodesIndexed: '3', changesIndexed: '2' }));
    expect((await nodeIndex.status()).changesIndexed).toBe('2');
  });
});
