// Item types (ADR 0119, 0128): the types of item a change may hold, declared by the domains (`itemTypes` of a domain,
// edited with it). An item's `type` is its item type, its `target` what it is about within that type (what an artifact
// is, the name of a signal). The graph lists the ones in force with what the platform code adds (graph.listItemTypes).
import type { ItemTypeInfo } from './api';
import { DEDICATED_TYPES } from './artifacts';

/** Types shown in a bubble by the subject they are about (ADR 0110). */
const BUBBLE_TYPES = ['assessment', 'comment'];

/** Where the change tab shows the items of a type. */
export function itemTypeShownIn(type: string): string {
  if (BUBBLE_TYPES.includes(type)) return 'a bubble next to the subject it is about';
  if (DEDICATED_TYPES.includes(type)) return 'a pane of its own in the change tab';
  return 'the raw list of the Artifacts pane';
}

/** What the platform code adds to a type, as read by people. */
export function itemTypeCodeLabel(x: ItemTypeInfo | undefined): string {
  if (!x) return 'not in force (the domain version declaring it is not published)';
  return x.code ? 'the platform code checks its items and gives them a meaning' : 'none: its items are data, checked against its attributes';
}

/** What writing an item of a type asks of its writer, as read by people. */
export function itemTypePermissionLabel(x: ItemTypeInfo | undefined): string {
  if (!x?.permission) return 'no permission of its own';
  return x.subjectField
    ? `the permission ${x.permission}, and the writer must be the principal named by data.${x.subjectField}`
    : `the permission ${x.permission}`;
}
