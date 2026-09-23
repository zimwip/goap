// Types of the IDE's mini-framework: views registered in zones, editor
// tabs, and toolbar contextual actions.
import type { Component } from 'svelte';
import type { IconName } from './Icon.svelte';

export type Zone = 'left' | 'right' | 'bottom' | 'editor';

export type TabParams = Record<string, string>;

/** What's needed to open a tab: the editor type and its params. */
export interface TabSpec {
  kind: string;
  params: TabParams;
}

export interface Tab extends TabSpec {
  /** `kind:key` — a single instance per object */
  id: string;
  /** false: preview tab, replaced by the next selection */
  pinned: boolean;
}

export interface ToolbarAction {
  id: string;
  label: string;
  icon?: IconName;
  run: () => unknown;
  disabled?: boolean;
  primary?: boolean;
  danger?: boolean;
  title?: string;
  /** shortcut shown in the tooltip */
  shortcut?: string;
}

/** Details shown by the "Properties" panel. */
export interface Properties {
  title: string;
  subtitle?: string;
  rows: [string, string][];
}

interface BaseView {
  id: string;
  title: string;
  icon: IconName;
  order?: number;
}

/** View of a side or bottom zone (no params). */
export interface PanelView extends BaseView {
  zone: 'left' | 'right' | 'bottom';
  component: Component;
  /** badge (counter) shown on the icon or tab */
  badge?: () => string | number | undefined;
}

/** Editor type: a component that receives the tab, plus hooks. */
export interface EditorView extends BaseView {
  zone: 'editor';
  component: Component<{ tab: Tab }>;
  /** unique key of the edited object (the tab id is `kind:key`) */
  key: (params: TabParams) => string;
  tabTitle: (tab: Tab) => string;
  tabIcon?: (tab: Tab) => IconName;
  /** tab tooltip */
  tooltip?: (tab: Tab) => string;
  dirty?: (tab: Tab) => boolean;
  /**
   * Groups tabs that share the same state (a methodology's draft): changes
   * are only discarded when the last tab of the group is closed.
   */
  group?: (tab: Tab) => string;
  /** does the group have changes (closing the group's last tab)? */
  groupDirty?: (tab: Tab) => boolean;
  /** discards the group's changes (close without saving) */
  discard?: (tab: Tab) => void;
  properties?: (tab: Tab) => Properties | undefined;
}

export type View = PanelView | EditorView;
