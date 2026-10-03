// "Expanded" state of the explorer trees (for the life of the page).
export const expanded: Record<string, boolean> = $state({});

export function toggle(key: string, def = false): void {
  expanded[key] = !(expanded[key] ?? def);
}

export function isOpen(key: string, def = false): boolean {
  return expanded[key] ?? def;
}
