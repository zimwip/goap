// The editors of the tabs of a change (ADR 0098): a tab of change objects names an editor (`additions.tabs[].editor` of a
// methodology, `editor` of a change object type); the editors register themselves here, as node editors do
// (ADR 0027), and a tab whose editor is unknown or empty is shown by the default object editor (ChangeObjects).
import type { Component } from 'svelte';
import type { ChangeObject } from './api';
import { openTab } from './shell/tabs.svelte';
import { OBJECT_PANE } from './changeObjects';

/** What a change tab editor is given. */
export interface ChangeTabProps {
  changeId: string;
  /** the change object types the tab shows */
  types: string[];
  /** the change objects of the change (every type: the editor keeps those of its types) */
  objects: ChangeObject[];
  /** the change takes no more writes (committed, applied, abandoned) */
  readonly: boolean;
  /** the workspace the change is seen from ('' : the main one) */
  workspace: string;
  /** called after a write, to reload the change objects */
  onchanged: () => void;
}

export interface ChangeTabEditor {
  name: string;
  title?: string;
  component: Component<ChangeTabProps>;
}

const editors = new Map<string, ChangeTabEditor>();

/** Registers the editor of a tab of change objects; a second one of the same name replaces it. */
export function registerChangeTab(e: ChangeTabEditor): void {
  editors.set(e.name, e);
}

/** The editor registered under a name (undefined: the default object editor). */
export function changeTabEditor(name: string | undefined): ChangeTabEditor | undefined {
  return name ? editors.get(name) : undefined;
}

/** Opens a change on a tab of its change objects (the id of an ObjectTab, or a change object type). */
export function openChangeTab(changeId: string, tab: string): void {
  const pane = tab.startsWith(OBJECT_PANE) ? tab : OBJECT_PANE + tab;
  openTab({ kind: 'change', params: { id: changeId, pane } }, { pin: true });
}
