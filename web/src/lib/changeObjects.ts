// The change objects of a change in the user interface (ADR 0098): what a change carries beyond its impacts, typed by
// the change object types of the domains. A change shows a tab per type of the change objects it holds, and the tabs
// the methodologies it carries declare (`additions.tabs`, shown even while empty). Pure: no store, no call.
import type { ChangeObject, ChangeObjectTypeInfo, ChangeTab, ObjectWrite } from './api';

/** A tab of the change objects of a change. */
export interface ObjectTab {
  /** the pane id: "objects:" then the declared tab's index, or the type */
  id: string;
  title: string;
  /** the editor that shows it ('' : the default object editor) */
  editor: string;
  /** the change object types it shows */
  types: string[];
  /** the change objects it shows */
  count: number;
  /** declared by a methodology (shown even while empty) */
  declared: boolean;
}

export const OBJECT_PANE = 'objects:';

/** The name of a type without its namespace. */
const nameOf = (ref: string) => ref.slice(ref.indexOf('@') + 1);

/**
 * The tabs of the change objects of a change: those its methodologies declare first, then one per type it holds change
 * objects of (or that `extra` names: a type the person is adding the first change object of) that no declared tab
 * shows. editorOf gives the editor a type names.
 */
export function objectTabs(objects: ChangeObject[], declared: ChangeTab[], editorOf: (type: string) => string = () => '', extra: string[] = []): ObjectTab[] {
  const count = (types: string[]) => objects.filter((o) => types.includes(o.type ?? '')).length;
  const out: ObjectTab[] = declared.map((t, i) => {
    const types = (t.objects ?? []).filter(Boolean);
    return { id: `${OBJECT_PANE}${i}`, title: t.title || types.map(nameOf).join(', '), editor: t.editor ?? '', types, count: count(types), declared: true };
  });
  const shown = new Set(out.flatMap((t) => t.types));
  const held = [...new Set([...objects.map((o) => o.type ?? ''), ...extra].filter((t) => t && !shown.has(t)))].sort();
  for (const t of held) out.push({ id: `${OBJECT_PANE}${t}`, title: nameOf(t), editor: editorOf(t), types: [t], count: count([t]), declared: false });
  return out;
}

/** The change objects of some types, in the order they were first written. */
export const objectsOfTypes = (objects: ChangeObject[], types: string[]): ChangeObject[] => objects.filter((o) => types.includes(o.type ?? ''));

/** How a change object is named in a list: its key, a singleton's type. */
export function keyLabel(o: ChangeObject): string {
  return o.key || nameOf(o.type ?? '');
}

/** Whether a new change object of the type needs a key from the person: a ref key does (the object it refers to). */
export function asksKey(t: ChangeObjectTypeInfo | undefined): boolean {
  return t?.key?.kind === 'ref';
}

/** The write that creates a change object of a type: a sequence allocates its key, a natural one makes it of the value. */
export function createWrite(type: string, value: Record<string, unknown>, key = '', workspace = ''): ObjectWrite {
  const w: ObjectWrite = { type, value };
  if (key) w.key = key;
  if (workspace) w.workspace = workspace;
  return w;
}

/** The write that edits a change object: the changed values merged into its last version, a transition when given. */
export function editWrite(o: ChangeObject, patch: Record<string, unknown>, transition = ''): ObjectWrite {
  const w: ObjectWrite = { type: o.type ?? '', merge: true, value: patch };
  if (o.key) w.key = o.key;
  if (o.workspace) w.workspace = o.workspace;
  if (transition) w.transition = transition;
  return w;
}

/** The transitions a change object can take from its state, by the lifecycle of its type. */
export function transitionsOf(t: ChangeObjectTypeInfo | undefined, state: string | undefined): { name: string; to: string }[] {
  const lc = t?.lifecycle;
  if (!lc || !state) return [];
  return (lc.transitions ?? [])
    .filter((tr) => tr.from === state)
    .map((tr) => ({ name: tr.name ?? '', to: tr.to ?? '' }))
    .filter((tr) => tr.name);
}
