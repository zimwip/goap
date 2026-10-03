// What an event of the platform stream touches, as the keys of the invalidation signals it moves.
import type { PlatformEvent } from './events.svelte';
import { keyOf } from './signals.svelte';

/** What an event touches: the keys of the signals it moves. */
export function keysOf(e: PlatformEvent): string[] {
  switch (e.kind) {
    case 'change':
      return [keyOf.change(e.id), keyOf.changes];
    case 'node':
      return [keyOf.node(e.id), ...(e.namespace ? [keyOf.namespace(e.namespace)] : []), ...(e.changeId ? [keyOf.change(e.changeId)] : []), ...(e.branch ? [keyOf.branch(e.branch)] : [])];
    case 'baseline':
      return [keyOf.baselines, ...(e.branch ? [keyOf.branch(e.branch)] : [])];
    case 'methodology':
      return [keyOf.methodology(e.id), keyOf.methodologies];
    case 'domain':
      return [keyOf.domain(e.id), keyOf.domains];
    default:
      return [];
  }
}
