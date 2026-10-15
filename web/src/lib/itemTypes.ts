// Item types (ADR 0119): the types of item a change may hold, as the graph lists them (graph.listItemTypes). An item's
// `type` is its item type, its `target` what it is about within that type (what an artifact is, the name of a signal).
import type { ItemTypeInfo } from './api';
import { DEDICATED_TYPES } from './artifacts';

/** Kinds shown in a bubble by the subject they are about (ADR 0110). */
const BUBBLE_TYPES = ['assessment', 'comment'];

/** Where the change tab shows the items of a type. */
export function itemTypeShownIn(type: string): string {
  if (BUBBLE_TYPES.includes(type)) return 'a bubble next to the subject it is about';
  if (DEDICATED_TYPES.includes(type)) return 'a pane of its own in the change tab';
  return 'the raw list of the Artifacts pane';
}

/** Origin as read by people: the core of the graph, or the use case registering the type. */
export const originLabel = (k: ItemTypeInfo): string => (k.origin === 'core' ? 'built-in' : k.origin ? `use case ${k.origin}` : 'registered');

/** The item types matching a filter (type, origin or description), in the order given. */
export function filterItemTypes(types: ItemTypeInfo[], filter: string): ItemTypeInfo[] {
  const q = filter.trim().toLowerCase();
  if (!q) return types;
  return types.filter((k) => `${k.type ?? ''} ${k.origin ?? ''} ${k.description ?? ''}`.toLowerCase().includes(q));
}
