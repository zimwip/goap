// Layout state: active tools, panel sizes and collapse, theme. Sizes, collapse and theme persist in
// localStorage; which tool is active does not (a new page opens on the default ones).
import { load, save } from './storage';

const KEY = 'goap.ide.layout';

export type Theme = 'auto' | 'light' | 'dark';

interface LayoutState {
  left: string;
  /** the navigation pinned beside the editor (else it shows over the editor from its rail) */
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
  leftOpen: false,
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

/** The navigation shown over the editor while not pinned (its rail hovered or focused, or a tool asked for). */
export const nav = $state({ revealed: false });

/** Click on a tool's icon: on the left, selects the tool and shows the navigation; on the right, opens or closes it. */
export function toggleTool(side: 'left' | 'right', id: string): void {
  if (side === 'left') {
    layout.left = id;
    if (!layout.leftOpen) nav.revealed = true;
  } else if (layout.right === id && layout.drawer) layout.drawer = false;
  else {
    layout.right = id;
    layout.drawer = true;
  }
}

export function showTool(side: 'left' | 'right' | 'bottom', id: string): void {
  if (side === 'left') {
    layout.left = id;
    if (!layout.leftOpen) nav.revealed = true;
  } else if (side === 'right') {
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
