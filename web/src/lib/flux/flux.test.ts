import { afterEach, describe, expect, it, vi } from 'vitest';
import { isOwnCommand, newCommandId } from './commands';
import { onKind, onPlatformEvent, onPresence, onResync, receive, resetStream, stream, type PlatformEvent } from './events.svelte';
import { keysOf } from './keys';
import { keyOf, stamp, throttled, touch, touchAll, touchSoon } from './signals.svelte';

const ev = (p: Partial<PlatformEvent>): PlatformEvent => ({
  type: '', kind: '', id: '', namespace: '', branch: '', project: '', changeId: '', version: 0, actor: '', commandId: '', time: '', label: '', presence: [], ...p,
});

afterEach(() => {
  resetStream();
  vi.useRealTimers();
});

describe('commands', () => {
  it('knows the ids it issued, and only those', () => {
    const id = newCommandId();
    expect(isOwnCommand(id)).toBe(true);
    expect(isOwnCommand('someone-else')).toBe(false);
    expect(isOwnCommand('')).toBe(false);
  });
  it('forgets the oldest beyond its memory', () => {
    const first = newCommandId();
    for (let i = 0; i < 600; i++) newCommandId();
    expect(isOwnCommand(first)).toBe(false);
  });
});

describe('signals', () => {
  it('moves the stamp of the key touched, and all with touchAll', () => {
    const k = 'test:a';
    const before = stamp(k);
    touch(k);
    expect(stamp(k)).toBe(before + 1);
    expect(stamp('test:other')).toBe(stamp('test:other2'));
    const other = stamp('test:other');
    touchAll();
    expect(stamp('test:other')).toBe(other + 1);
  });
  it('coalesces a burst into one touch', () => {
    vi.useFakeTimers();
    const k = 'test:burst';
    const before = stamp(k);
    for (let i = 0; i < 20; i++) touchSoon(k, 100);
    vi.advanceTimersByTime(99);
    expect(stamp(k)).toBe(before);
    vi.advanceTimersByTime(2);
    expect(stamp(k)).toBe(before + 1);
  });
});

describe('keysOf', () => {
  it('maps events to the signals they move', () => {
    expect(keysOf(ev({ kind: 'change', id: 'C1' }))).toEqual([keyOf.change('C1'), keyOf.changes]);
    expect(keysOf(ev({ kind: 'node', id: 'N1', namespace: 'alm', changeId: 'C1', branch: 'b' }))).toEqual([
      keyOf.node('N1'),
      keyOf.namespace('alm'),
      keyOf.change('C1'),
      keyOf.branch('b'),
    ]);
    expect(keysOf(ev({ kind: 'baseline', branch: 'main' }))).toEqual([keyOf.baselines, keyOf.branch('main')]);
    expect(keysOf(ev({ kind: 'methodology', id: 'sdlc' }))).toEqual([keyOf.methodology('sdlc'), keyOf.methodologies]);
    expect(keysOf(ev({ kind: 'domain', id: 'alm' }))).toEqual([keyOf.domain('alm'), keyOf.domains]);
    expect(keysOf(ev({ kind: 'process', id: 'p' }))).toEqual([]);
  });
});

describe('event stream', () => {
  it('tracks epoch and seq, and dispatches by kind', () => {
    const all: string[] = [];
    const nodes: string[] = [];
    const offA = onPlatformEvent((e) => all.push(e.type));
    const offB = onKind('node', (e) => nodes.push(e.id));
    receive({ epoch: 'e1', seq: '1', type: 'node.written', kind: 'node', id: 'N1', version: '3' });
    receive({ epoch: 'e1', seq: '2', type: 'change.created', kind: 'change', id: 'C1' });
    expect(stream.epoch).toBe('e1');
    expect(stream.seq).toBe(2);
    expect(all).toEqual(['node.written', 'change.created']);
    expect(nodes).toEqual(['N1']);
    offA();
    offB();
  });
  it('resyncs on "resync", and on a heartbeat that is ahead of what was seen', () => {
    const resync = vi.fn();
    const off = onResync(resync);
    receive({ epoch: 'e1', seq: '5', type: 'node.written', kind: 'node', id: 'N' });
    receive({ epoch: 'e1', seq: '5', type: 'heartbeat' });
    expect(resync).not.toHaveBeenCalled();
    receive({ epoch: 'e1', seq: '9', type: 'heartbeat' });
    expect(resync).toHaveBeenCalledTimes(1);
    expect(stream.seq).toBe(9);
    receive({ epoch: 'e2', type: 'resync' });
    expect(resync).toHaveBeenCalledTimes(2);
    expect(stream.epoch).toBe('e2');
    expect(stream.seq).toBe(0);
    off();
  });
  it('starts over on another epoch', () => {
    receive({ epoch: 'e1', seq: '40', type: 'node.written', kind: 'node', id: 'N' });
    receive({ epoch: 'e2', seq: '1', type: 'node.written', kind: 'node', id: 'N' });
    expect(stream.epoch).toBe('e2');
    expect(stream.seq).toBe(1);
  });
  it('sends presence events to the presence listeners only', () => {
    const seen: string[] = [];
    const generic = vi.fn();
    const offP = onPresence((type, list) => seen.push(`${type}:${list.map((p) => p.subject).join(',')}`));
    const offE = onPlatformEvent(generic);
    receive({ epoch: 'e', type: 'presence.joined', kind: 'presence', presence: [{ tabId: 't', subject: 'bob', kind: 'node', id: 'N1' }] });
    expect(seen).toEqual(['presence.joined:bob']);
    expect(generic).not.toHaveBeenCalled();
    offP();
    offE();
  });
});

describe('throttled', () => {
  it('runs at once, then at most once per interval, the calls in between collapsing into one', () => {
    vi.useFakeTimers();
    let t = 0;
    const fn = vi.fn();
    const th = throttled(fn, 1000, () => t);
    th.call();
    expect(fn).toHaveBeenCalledTimes(1);
    for (let i = 0; i < 20; i++) {
      t += 40;
      th.call();
    }
    expect(fn).toHaveBeenCalledTimes(1);
    t = 1000;
    vi.advanceTimersByTime(1000);
    expect(fn).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(5000);
    expect(fn).toHaveBeenCalledTimes(2);
    t = 5000;
    th.call();
    expect(fn).toHaveBeenCalledTimes(3);
  });
  it('can be cancelled', () => {
    vi.useFakeTimers();
    let t = 0;
    const fn = vi.fn();
    const th = throttled(fn, 1000, () => t);
    th.call();
    t = 10;
    th.call();
    th.cancel();
    vi.advanceTimersByTime(5000);
    expect(fn).toHaveBeenCalledTimes(1);
  });
});
