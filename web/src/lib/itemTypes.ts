// Item types (ADR 0119): the kinds of item a change may hold, as the graph lists them (graph.listItemKinds). "Item
// type" is the name people read; the code and the wire keep `kind`, `type` being the sub-type of an item (the type of
// an artifact, the name of a signal).
import type { ItemKindInfo } from './api';
import { DEDICATED_KINDS } from './artifacts';

/** Kinds shown in a bubble by the subject they are about (ADR 0110). */
const BUBBLE_KINDS = ['assessment', 'comment'];

/** Where the change tab shows the items of a kind. */
export function itemTypeShownIn(kind: string): string {
  if (BUBBLE_KINDS.includes(kind)) return 'a bubble next to the subject it is about';
  if (DEDICATED_KINDS.includes(kind)) return 'a pane of its own in the change tab';
  return 'the raw list of the Artifacts pane';
}

/** Origin as read by people: the core of the graph, or the use case registering the kind. */
export const originLabel = (k: ItemKindInfo): string => (k.origin === 'core' ? 'built-in' : k.origin ? `use case ${k.origin}` : 'registered');

/** The item types matching a filter (kind, origin or description), in the order given. */
export function filterItemTypes(kinds: ItemKindInfo[], filter: string): ItemKindInfo[] {
  const q = filter.trim().toLowerCase();
  if (!q) return kinds;
  return kinds.filter((k) => `${k.kind ?? ''} ${k.origin ?? ''} ${k.description ?? ''}`.toLowerCase().includes(q));
}
