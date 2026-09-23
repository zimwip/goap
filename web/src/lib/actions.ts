// Svelte utility actions.

/**
 * Click delegation on `[data-row]` table rows (keyboard access goes through a
 * button inside the row). Clicks on a control within the row are ignored.
 */
export function rowClick(node: HTMLElement, handler: (row: HTMLElement, e: MouseEvent) => void) {
  let h = handler;
  const onClick = (e: MouseEvent) => {
    const t = e.target as HTMLElement;
    const row = t.closest<HTMLElement>('[data-row]');
    if (!row || !node.contains(row) || t.closest('button, a, input, select, textarea')) return;
    h(row, e);
  };
  node.addEventListener('click', onClick);
  return {
    update(next: (row: HTMLElement, e: MouseEvent) => void) {
      h = next;
    },
    destroy() {
      node.removeEventListener('click', onClick);
    },
  };
}
