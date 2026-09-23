// Actions Svelte utilitaires.

/**
 * Délégation du clic sur les lignes `[data-row]` d'un tableau (le clavier passe
 * par un bouton dans la ligne). Les clics sur un contrôle de la ligne sont ignorés.
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
