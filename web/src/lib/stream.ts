// Connect server streaming from the browser, without code generation:
// `POST /{Service}/{Method}` with `Content-Type: application/connect+json`.
//
// Request: ONE envelope = 1 flags byte (0) + 4-byte length (big-endian) +
// UTF-8 JSON. Response: a sequence of envelopes; flag 0x02 marks the
// end-of-stream envelope, whose JSON may contain
// `{"error": {"code", "message"}}`.

import { BASE, RpcError, getToken, ENGINE_SERVICE, type WatchEvent } from './api';

const FLAG_COMPRESSED = 0x01;
const FLAG_END_STREAM = 0x02;

function envelope(json: unknown): Uint8Array<ArrayBuffer> {
  const payload = new TextEncoder().encode(JSON.stringify(json));
  const out = new Uint8Array(5 + payload.length);
  out[0] = 0;
  new DataView(out.buffer).setUint32(1, payload.length, false);
  out.set(payload, 5);
  return out;
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  if (a.length === 0) return b;
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}

/**
 * Calls a server-streaming RPC and forwards each message to `onMessage`.
 * Resolves on normal stream end; rejects with an `RpcError` if the server
 * reports an error or if the stream is cut before its end-of-stream envelope.
 * `onOpen` is called as soon as the response headers are received.
 */
export async function serverStream<TReq extends object, TRes>(
  service: string,
  method: string,
  body: TReq,
  onMessage: (msg: TRes) => void,
  signal: AbortSignal,
  onOpen?: () => void,
): Promise<void> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/connect+json',
    'Connect-Protocol-Version': '1',
  };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;

  let res: Response;
  try {
    res = await fetch(`${BASE}/${service}/${method}`, {
      method: 'POST',
      headers,
      body: envelope(body),
      signal,
    });
  } catch (e) {
    if (signal.aborted) throw e;
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }

  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '');
    let err: { code?: string; message?: string } = {};
    try {
      err = JSON.parse(text) as typeof err;
    } catch {
      // non-JSON body
    }
    throw new RpcError(err.code ?? (res.status === 404 ? 'unimplemented' : 'unknown'), err.message ?? (text || res.statusText), res.status);
  }
  onOpen?.();

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf: Uint8Array = new Uint8Array(0);
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf = concat(buf, value);
      while (buf.length >= 5) {
        const flags = buf[0];
        const len = new DataView(buf.buffer, buf.byteOffset, buf.byteLength).getUint32(1, false);
        if (buf.length < 5 + len) break;
        const payload = buf.subarray(5, 5 + len);
        buf = buf.slice(5 + len);
        if (flags & FLAG_COMPRESSED) throw new RpcError('internal', 'Compressed message not supported', res.status);
        const text = decoder.decode(payload);
        if (flags & FLAG_END_STREAM) {
          let end: { error?: { code?: string; message?: string } } = {};
          try {
            end = text ? (JSON.parse(text) as typeof end) : {};
          } catch {
            // unreadable end-of-stream: treated as normal
          }
          if (end.error) throw new RpcError(end.error.code ?? 'unknown', end.error.message ?? '', res.status);
          return;
        }
        onMessage(JSON.parse(text) as TRes);
      }
    }
  } finally {
    reader.releaseLock();
  }
  if (signal.aborted) return;
  throw new RpcError('unavailable', 'Stream interrupted before its end', res.status);
}

// ---------------------------------------------------------------------------
// Durable subscription with reconnection
// ---------------------------------------------------------------------------

export type StreamStatus = 'connecting' | 'open' | 'retrying' | 'stopped';

export interface WatchOptions {
  /** empty: all processes readable by the caller */
  processId?: string;
  onEvent: (e: WatchEvent) => void;
  onStatus?: (status: StreamStatus, error?: RpcError) => void;
}

const MIN_DELAY = 1000;
const MAX_DELAY = 30_000;

/**
 * Subscribes to `EngineService.WatchEvents` and reconnects with an
 * exponential delay when the stream ends or fails. Returns the stop function.
 */
export function watchEvents(opts: WatchOptions): () => void {
  const ctrl = new AbortController();
  let delay = MIN_DELAY;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const status = (s: StreamStatus, e?: RpcError) => {
    if (!ctrl.signal.aborted || s === 'stopped') opts.onStatus?.(s, e);
  };

  async function loop() {
    while (!ctrl.signal.aborted) {
      status('connecting');
      let failure: RpcError | undefined;
      try {
        await serverStream(
          ENGINE_SERVICE,
          'WatchEvents',
          opts.processId ? { processId: opts.processId } : {},
          (msg: WatchEvent) => {
            delay = MIN_DELAY; // a message was received: the connection is healthy
            opts.onEvent(msg);
          },
          ctrl.signal,
          () => status('open'),
        );
      } catch (e) {
        if (ctrl.signal.aborted) break;
        failure = e instanceof RpcError ? e : new RpcError('unknown', String(e), 0);
      }
      if (ctrl.signal.aborted) break;
      status('retrying', failure);
      // Access denials and missing RPCs don't get fixed quickly: wait longer.
      const slow = failure && ['unauthenticated', 'permission_denied', 'unimplemented'].includes(failure.code);
      const wait = slow ? MAX_DELAY : delay;
      delay = Math.min(delay * 2, MAX_DELAY);
      await new Promise<void>((resolve) => {
        timer = setTimeout(resolve, wait + Math.random() * 250);
        ctrl.signal.addEventListener('abort', () => resolve(), { once: true });
      });
    }
    status('stopped');
  }

  void loop();
  return () => {
    clearTimeout(timer);
    ctrl.abort();
  };
}
