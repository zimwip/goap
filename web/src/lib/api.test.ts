import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { COMMAND_HEADER } from './flux/commands';
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
