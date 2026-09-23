// État de la disposition : outils actifs, tailles et repli des panneaux,
// thème. Persisté dans localStorage.
import { load, save } from './storage';

const KEY = 'goap.ide.layout';

export type Theme = 'auto' | 'light' | 'dark';

interface LayoutState {
  left: string;
  leftOpen: boolean;
  leftWidth: number;
  right: string;
  rightOpen: boolean;
  rightWidth: number;
  bottom: string;
  bottomOpen: boolean;
  bottomHeight: number;
  theme: Theme;
}

const DEFAULTS: LayoutState = {
  left: 'methodologies',
  leftOpen: true,
  leftWidth: 280,
  right: 'tester',
  rightOpen: false,
  rightWidth: 340,
  bottom: 'events',
  bottomOpen: true,
  bottomHeight: 220,
  theme: 'auto',
};

export const layout: LayoutState = $state(load(KEY, DEFAULTS));

export const LIMITS = {
  side: { min: 180, max: 640 },
  bottom: { min: 100, max: 800 },
};

$effect.root(() => {
  $effect(() => {
    save(KEY, { ...layout });
  });
  $effect(() => {
    const t = layout.theme;
    if (t === 'auto') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.dataset.theme = t;
  });
});

/** Clic sur une icône de la barre d'activité : sélectionne l'outil ou replie le panneau. */
export function toggleTool(side: 'left' | 'right', id: string): void {
  if (side === 'left') {
    if (layout.left === id && layout.leftOpen) layout.leftOpen = false;
    else {
      layout.left = id;
      layout.leftOpen = true;
    }
  } else if (layout.right === id && layout.rightOpen) layout.rightOpen = false;
  else {
    layout.right = id;
    layout.rightOpen = true;
  }
}

export function showTool(side: 'left' | 'right' | 'bottom', id: string): void {
  if (side === 'left') {
    layout.left = id;
    layout.leftOpen = true;
  } else if (side === 'right') {
    layout.right = id;
    layout.rightOpen = true;
  } else {
    layout.bottom = id;
    layout.bottomOpen = true;
  }
}

export function toggleConsole(): void {
  layout.bottomOpen = !layout.bottomOpen;
}

export function clamp(v: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, v));
}
