// Layout state: active tools, panel sizes and collapse, theme. Sizes, collapse and theme persist in
// localStorage; which tool is active does not (a new page opens on the default ones).
import { load, save } from './storage';
import { panelView } from './registry';

const KEY = 'goap.ide.layout';

export type Theme = 'auto' | 'light' | 'dark';

interface LayoutState {
  /** the navigation tool of the left side */
  left: string;
  /** the left tool filling the editor space (a `fill` view), '' while the editors are shown */
  full: string;
  /** the navigation of the left tool shown beside the editors (hidden: the tool bar only) */
  leftOpen: boolean;
  leftWidth: number;
  /** the side tool shown (Test, Properties, DSL Help), opened from the status bar */
  right: string;
  /** whether that tool is shown */
  drawer: boolean;
  /** the context bar pinned open beside the editor */
  rightOpen: boolean;
  /** the width of the side tool */
  rightWidth: number;
  /** the width of the context bar, open */
  contextWidth: number;
  bottom: string;
  bottomOpen: boolean;
  bottomHeight: number;
  theme: Theme;
}

const DEFAULTS: LayoutState = {
  left: 'methodologies',
  full: '',
  leftOpen: true,
  leftWidth: 280,
  right: 'tester',
  drawer: false,
  rightOpen: false,
  rightWidth: 340,
  contextWidth: 248,
  bottom: 'events',
  bottomOpen: true,
  bottomHeight: 220,
  theme: 'auto',
};

/** What the browser keeps: how the workbench looks, not where the user was. */
const kept = (l: LayoutState) => ({
  leftOpen: l.leftOpen,
  leftWidth: l.leftWidth,
  rightOpen: l.rightOpen,
  rightWidth: l.rightWidth,
  contextWidth: l.contextWidth,
  bottomOpen: l.bottomOpen,
  bottomHeight: l.bottomHeight,
  theme: l.theme,
});

export const layout: LayoutState = $state({ ...DEFAULTS, ...kept(load(KEY, DEFAULTS)) });

/** The width of the context bar closed: its rail of icons. */
export const RAIL = 44;

export const LIMITS = {
  context: { min: 200, max: 420 },
  side: { min: 180, max: 640 },
  bottom: { min: 100, max: 800 },
};

$effect.root(() => {
  $effect(() => {
    save(KEY, kept(layout));
  });
  $effect(() => {
    const t = layout.theme;
    if (t === 'auto') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.dataset.theme = t;
  });
});

/** Whether a left tool fills the editor space rather than showing a navigation. */
export const fills = (id: string): boolean => !!panelView(id)?.fill;

/**
 * Click on a tool's icon. On the left, the bar selects a tool: a navigation tool shows its navigation beside the editors
 * (a second click on it hides the navigation), a tool filling the editor space shows there with no navigation (a second
 * click gives the space back to the editors). On the right, opens or closes the side tool.
 */
export function toggleTool(side: 'left' | 'right', id: string): void {
  if (side === 'left' && fills(id)) layout.full = layout.full === id ? '' : id;
  else if (side === 'left') {
    if (layout.left === id && layout.leftOpen && !layout.full) layout.leftOpen = false;
    else selectNavigation(id);
  } else if (layout.right === id && layout.drawer) layout.drawer = false;
  else {
    layout.right = id;
    layout.drawer = true;
  }
}

/** Shows a navigation tool beside the editors (taking the space back from a tool filling it). */
function selectNavigation(id: string): void {
  layout.left = id;
  layout.leftOpen = true;
  layout.full = '';
}

export function showTool(side: 'left' | 'right' | 'bottom', id: string): void {
  if (side === 'left' && fills(id)) layout.full = id;
  else if (side === 'left') selectNavigation(id);
  else if (side === 'right') {
    layout.right = id;
    layout.drawer = true;
  } else {
    layout.bottom = id;
    layout.bottomOpen = true;
  }
}

export function toggleConsole(): void {
  layout.bottomOpen = !layout.bottomOpen;
}

/** Shows or hides the navigation of the left tool (Ctrl+B); over a tool filling the editor space, shows it with the editors. */
export function toggleNavigation(): void {
  if (layout.full) selectNavigation(layout.left);
  else layout.leftOpen = !layout.leftOpen;
}
