// State of the node search overlay (mounted once at the shell root).
export const searchOverlay = $state({ open: false, q: '' });

export function openSearch(q = ''): void {
  searchOverlay.q = q;
  searchOverlay.open = true;
}

export function closeSearch(): void {
  searchOverlay.open = false;
}
