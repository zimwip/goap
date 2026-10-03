// Invalidation signals: what an event says changed, as a counter per thing. A view that shows a change, a node, a
// branch… reads `stamp(key)` in the `$effect` that loads it, and loads again when it moves — no `reload++`, no
// refetch written after each mutation. Keys are built by the helpers below.
import { SvelteMap } from 'svelte/reactivity';

const stamps = new SvelteMap<string, number>();
/** moves when everything must be read again (the stream could not vouch for what it missed) */
let generation = $state(0);

/** Reactive: moves each time `touch(key)` or `touchAll()` is called. 0 until then. */
export function stamp(key: string): number {
  return (stamps.get(key) ?? 0) + generation;
}

/** Everything is stale: the stream restarted after a gap, or the session changed. */
export function touchAll(): void {
  generation++;
}

const timers = new Map<string, ReturnType<typeof setTimeout>>();

/** `touch` once the events of a burst stopped coming (a change applying writes dozens of nodes). */
export function touchSoon(key: string, ms = 120): void {
  clearTimeout(timers.get(key));
  timers.set(
    key,
    setTimeout(() => {
      timers.delete(key);
      touch(key);
    }, ms),
  );
}

/** Says something changed (reducers call it, from the events). */
export function touch(key: string): void {
  stamps.set(key, (stamps.get(key) ?? 0) + 1);
}

export const keyOf = {
  change: (id: string) => `change:${id}`,
  node: (id: string) => `node:${id}`,
  /** a branch of the graph moved (a baseline was created on it) */
  branch: (name: string) => `branch:${name}`,
  /** a node of the namespace was written (organisation, platform, alm…) */
  namespace: (ns: string) => `ns:${ns}`,
  /** any baseline anywhere */
  baselines: 'baselines',
  /** any change, any status: the list changed */
  changes: 'changes',
  methodology: (name: string) => `methodology:${name}`,
  methodologies: 'methodologies',
  domain: (name: string) => `domain:${name}`,
  domains: 'domains',
};
