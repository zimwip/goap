// Contributions des éditeurs actifs à l'atelier : actions de la barre d'outils,
// demandes de mise en évidence d'un champ, sélection pour « Propriétés »,
// notifications.
import { SvelteMap } from 'svelte/reactivity';
import { tick } from 'svelte';
import type { Properties, ToolbarAction } from './types';
import { parentPath } from '../methodologyForm';

// --- actions contextuelles ------------------------------------------------------

/** Actions fournies par l'éditeur monté de chaque onglet. */
export const tabActions = new SvelteMap<string, ToolbarAction[]>();

/**
 * À appeler à l'initialisation d'un composant d'éditeur : publie ses actions
 * pour la barre d'outils (recalculées quand l'état lu par `fn` change).
 */
export function provideActions(tabId: () => string, fn: () => ToolbarAction[]): void {
  $effect(() => {
    const id = tabId();
    tabActions.set(id, fn());
  });
  $effect(() => {
    const id = tabId();
    return () => {
      tabActions.delete(id);
    };
  });
}

/** Exécute l'action `id` de l'onglet (raccourcis clavier). Renvoie false si absente. */
export function runTabAction(tabIdValue: string, id: string): boolean {
  const a = tabActions.get(tabIdValue)?.find((x) => x.id === id);
  if (!a || a.disabled) return false;
  void a.run();
  return true;
}

// --- mise en évidence d'un champ ---------------------------------------------------

export const revealState = $state({ tabId: '', path: '', seq: 0 });

export function requestReveal(tabId: string, path: string): void {
  revealState.tabId = tabId;
  revealState.path = path;
  revealState.seq += 1;
}

/** Amène à l'écran (et donne le focus à) l'élément `[data-path]` le plus proche du chemin. */
export function revealIn(root: HTMLElement | undefined, path: string): boolean {
  if (!root) return false;
  const els = [...root.querySelectorAll<HTMLElement>('[data-path]')];
  for (let p = path; p; p = parentPath(p)) {
    const el = els.find((e) => e.dataset.path === p);
    if (!el) continue;
    el.scrollIntoView({ behavior: 'smooth', block: 'center' });
    const focusable = el.matches('input, select, textarea, button')
      ? el
      : el.querySelector<HTMLElement>('.cm-content, input, select, textarea');
    focusable?.focus({ preventScroll: true });
    el.classList.remove('flash');
    void el.offsetWidth;
    el.classList.add('flash');
    return true;
  }
  return false;
}

/**
 * À appeler dans un éditeur : traite les demandes de mise en évidence qui le
 * concernent (après le rendu).
 */
export function useReveal(tabId: () => string, root: () => HTMLElement | undefined): void {
  $effect(() => {
    void revealState.seq;
    const id = tabId();
    const el = root();
    if (!el || revealState.tabId !== id || !revealState.path) return;
    const path = revealState.path;
    revealState.path = '';
    void tick().then(() => revealIn(el, path));
  });
}

// --- sélection ---------------------------------------------------------------------

export const selection = $state<{ current: Properties | undefined }>({ current: undefined });

export function select(p: Properties | undefined): void {
  selection.current = p;
}

// --- notifications -------------------------------------------------------------------

export interface Toast {
  id: number;
  tone: 'ok' | 'error' | 'info';
  text: string;
}

export const toasts = $state<Toast[]>([]);
let toastSeq = 0;

export function notify(text: string, tone: Toast['tone'] = 'info', ms = 4000): void {
  const id = ++toastSeq;
  toasts.push({ id, tone, text });
  setTimeout(() => dismiss(id), tone === 'error' ? ms * 2 : ms);
}

export function dismiss(id: number): void {
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}

// --- requêtes transverses -------------------------------------------------------------

/** Compteur incrémenté pour demander le focus de l'intention dans « Tester ». */
export const focusRequests = $state({ tester: 0, search: 0 });
