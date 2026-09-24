// Right-click context menu: a single global menu, positioned at the click,
// its items supplied by whoever opened it (a tree row, a tab…).
import type { IconName } from './Icon.svelte';

export interface ContextMenuItem {
  label: string;
  icon?: IconName;
  /** Shown but not clickable (e.g. missing permission). */
  disabled?: boolean;
  danger?: boolean;
  run: () => void;
}

export const menuState = $state<{ x: number; y: number; items: ContextMenuItem[] }>({ x: 0, y: 0, items: [] });

/** Opens the menu at the mouse event's position, replacing any open menu. */
export function openContextMenu(e: MouseEvent, items: ContextMenuItem[]): void {
  e.preventDefault();
  e.stopPropagation();
  menuState.x = e.clientX;
  menuState.y = e.clientY;
  menuState.items = items;
}

export function closeContextMenu(): void {
  menuState.items = [];
}
