// What an event of the platform stream touches, as the keys of the invalidation signals it moves.
import type { PlatformEvent } from './events.svelte';
import { keyOf } from './signals.svelte';

/** the conversation events that change the caller's list of conversations */
const LIST_EVENTS = new Set(['conversation.created', 'conversation.renamed', 'conversation.deleted']);

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
    case 'conversation':
      // the stream delivers them to their owner only (ADR 0136); the list moves when one comes, goes or is renamed
      return [keyOf.conversation(e.id), ...(LIST_EVENTS.has(e.type) ? [keyOf.conversations] : [])];
    case 'job':
      return [keyOf.job(e.id), keyOf.jobs];
    case 'model':
      return [keyOf.usage];
    case 'connector':
      return [keyOf.connectors];
    case 'preferences':
      return [keyOf.preferences];
    default:
      return [];
  }
}
