import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { COMMAND_HEADER } from './flux/commands';
import { AT_LEAST_HEADER, CHANGE_SEQ_HEADER, MAX_POSITIONS, atLeastFor, notePosition, parsePositions, positionOf, resetPositions } from './flux/positions';
import { RpcError, errorMessage, getToken, onTokenChange, onUnauthorized, reportUnauthorized, rpc, setToken } from './api';

// the transport: what every call of the web goes through (rpc, the token store, the 401 listeners)

function storage() {
  const m = new Map<string, string>();
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
  };
}

function reply(status: number, body: unknown, statusText = '') {
  return Promise.resolve(new Response(typeof body === 'string' ? body : JSON.stringify(body), { status, statusText }));
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.stubGlobal('localStorage', storage());
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('rpc', () => {
  it('posts the JSON body to /service/method and returns the decoded answer', async () => {
    fetchMock.mockReturnValue(reply(200, { names: ['a'] }));
    const out = await rpc<{ q: string }, { names: string[] }>('goap.x.v1.Svc', 'List', { q: 'x' });
    expect(out).toEqual({ names: ['a'] });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/goap.x.v1.Svc/List');
    expect(init.method).toBe('POST');
    expect(init.body).toBe('{"q":"x"}');
    expect(init.headers['Content-Type']).toBe('application/json');
    expect(init.headers[COMMAND_HEADER]).toBeTruthy();
    expect(init.headers['Authorization']).toBeUndefined();
  });

  it('answers an empty object for an empty body, and gives each call its own command id', async () => {
    fetchMock.mockImplementation(() => reply(200, ''));
    expect(await rpc('s', 'm', {})).toEqual({});
    await rpc('s', 'm', {});
    expect(fetchMock.mock.calls[0][1].headers[COMMAND_HEADER]).not.toBe(fetchMock.mock.calls[1][1].headers[COMMAND_HEADER]);
  });

  it('sends the token as a bearer', async () => {
    setToken('tok');
    fetchMock.mockReturnValue(reply(200, {}));
    await rpc('s', 'm', {});
    expect(fetchMock.mock.calls[0][1].headers['Authorization']).toBe('Bearer tok');
  });

  it('maps an error answer to an RpcError with the code and message of the body', async () => {
    fetchMock.mockReturnValue(reply(403, { code: 'permission_denied', message: 'nope' }));
    const e = await rpc('s', 'm', {}).catch((x) => x);
    expect(e).toBeInstanceOf(RpcError);
    expect(e).toMatchObject({ code: 'permission_denied', message: 'nope', status: 403 });
  });

  it('falls back to the text, the status text and a code derived from the status', async () => {
    fetchMock.mockReturnValueOnce(reply(500, 'boom'));
    expect(await rpc('s', 'm', {}).catch((x) => x)).toMatchObject({ code: 'unknown', message: 'boom', status: 500 });
    fetchMock.mockReturnValueOnce(reply(401, ''));
    expect(await rpc('s', 'm', {}).catch((x) => x)).toMatchObject({ code: 'unauthenticated', status: 401 });
  });

  it('maps an unreachable gateway to an unavailable RpcError, and lets an abort through', async () => {
    fetchMock.mockRejectedValueOnce(new TypeError('failed to fetch'));
    expect(await rpc('s', 'm', {}).catch((x) => x)).toMatchObject({ code: 'unavailable', status: 0 });
    fetchMock.mockRejectedValueOnce(new DOMException('aborted', 'AbortError'));
    expect(await rpc('s', 'm', {}).catch((x) => x)).toMatchObject({ name: 'AbortError' });
  });
});

describe('401 handling', () => {
  it('reports a refused token to the listeners', async () => {
    const seen: string[] = [];
    const off = onUnauthorized((m) => seen.push(m));
    setToken('tok');
    fetchMock.mockReturnValue(reply(401, { code: 'unauthenticated', message: 'token expired' }));
    await rpc('s', 'm', {}).catch(() => undefined);
    expect(seen).toEqual(['token expired']);
    off();
    await rpc('s', 'm', {}).catch(() => undefined);
    expect(seen).toHaveLength(1);
  });

  it('ignores a 401 without a token, for another token, or another status', () => {
    const seen: string[] = [];
    const off = onUnauthorized((m) => seen.push(m));
    setToken('current');
    reportUnauthorized(null, 401, 'x');
    reportUnauthorized('old', 401, 'x');
    reportUnauthorized('current', 403, 'x');
    expect(seen).toEqual([]);
    reportUnauthorized('current', 401, 'ended');
    expect(seen).toEqual(['ended']);
    off();
  });
});

describe('token store', () => {
  it('keeps the token and tells the listeners on every change', () => {
    let n = 0;
    const off = onTokenChange(() => n++);
    expect(getToken()).toBeNull();
    setToken('a');
    expect(getToken()).toBe('a');
    setToken(null);
    expect(getToken()).toBeNull();
    expect(n).toBe(2);
    off();
    setToken('b');
    expect(n).toBe(2);
  });

  it('survives an unavailable storage', () => {
    const blocked = () => {
      throw new Error('blocked');
    };
    vi.stubGlobal('localStorage', { getItem: blocked, setItem: blocked, removeItem: blocked });
    expect(getToken()).toBeNull();
    expect(() => setToken('x')).not.toThrow();
  });
});

describe('errorMessage', () => {
  it('words the access errors for the UI', () => {
    expect(errorMessage(new RpcError('permission_denied', 'why', 403))).toContain('Access denied');
    expect(errorMessage(new RpcError('unauthenticated', '', 401))).toBe('Authentication required: sign in again.');
    expect(errorMessage(new RpcError('not_found', 'gone', 404))).toBe('not_found: gone');
    expect(errorMessage(new Error('plain'))).toBe('plain');
    expect(errorMessage('str')).toBe('str');
  });
});

describe('positions of the logs of the changes (ADR 0153)', () => {
  const G = 'goap.graph.v1.GraphService';
  const withHeaders = (status: number, body: unknown, headers: Record<string, string>) =>
    Promise.resolve(new Response(JSON.stringify(body), { status, headers }));
  afterEach(() => resetPositions());

  it('notes where a write left the logs and asks the next reads of those changes to reach it', async () => {
    fetchMock.mockReturnValueOnce(withHeaders(200, {}, { [CHANGE_SEQ_HEADER]: 'C1:7,C2:3' }));
    await rpc(G, 'AddItems', { changeId: 'C1' });
    expect(positionOf('C1')).toBe(7);
    fetchMock.mockImplementation(() => reply(200, {}));
    await rpc(G, 'GetChange', { id: 'C1' });
    expect(fetchMock.mock.calls[1][1].headers[AT_LEAST_HEADER]).toBe('C1:7');
    await rpc(G, 'GetChange', { id: 'C9' });
    expect(fetchMock.mock.calls[2][1].headers[AT_LEAST_HEADER]).toBeUndefined();
    await rpc('goap.engine.v1.EngineService', 'GetChangeState', { changeId: 'C1' });
    expect(fetchMock.mock.calls[3][1].headers[AT_LEAST_HEADER]).toBeUndefined();
  });

  it('reads again a change whose view is behind', async () => {
    vi.useFakeTimers();
    notePosition('C1', 5);
    fetchMock
      .mockReturnValueOnce(withHeaders(409, { code: 'aborted', message: 'behind' }, { 'Goap-Error-Reason': 'behind' }))
      .mockReturnValueOnce(reply(200, { change: { id: 'C1', seq: '5' } }));
    const p = rpc(G, 'GetChange', { id: 'C1' });
    await vi.runAllTimersAsync();
    await expect(p).resolves.toEqual({ change: { id: 'C1', seq: '5' } });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it('gives up on a view still behind after its retries', async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementation(() => withHeaders(409, { code: 'aborted', message: 'behind' }, { 'Goap-Error-Reason': 'behind' }));
    const p = rpc(G, 'GetChange', { id: 'C1' }).catch((e) => e);
    await vi.runAllTimersAsync();
    const e = await p;
    expect(e).toBeInstanceOf(RpcError);
    expect((e as RpcError).reason).toBe('behind');
    expect(fetchMock).toHaveBeenCalledTimes(4);
    vi.useRealTimers();
  });
});

describe('positions', () => {
  afterEach(() => resetPositions());
  it('keeps the highest position, parses the header and forgets the oldest beyond its memory', () => {
    notePosition('A', 4);
    notePosition('A', 2);
    expect(positionOf('A')).toBe(4);
    expect([...parsePositions('A:1, B:x,:3,C:2')]).toEqual([['A', 1], ['C', 2]]);
    expect(atLeastFor({ changeId: 'A', id: 'A', parentId: 'Z' })).toBe('A:4');
    for (let i = 0; i < MAX_POSITIONS; i++) notePosition(`X${i}`, 1);
    expect(positionOf('A')).toBe(0);
  });
});
