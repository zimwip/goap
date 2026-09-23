// Contributions of active editors to the workbench: toolbar actions,
// field-highlight requests, selection for "Properties", notifications.
import { SvelteMap } from 'svelte/reactivity';
import { tick } from 'svelte';
import type { Properties, ToolbarAction } from './types';
import { parentPath } from '../methodologyForm';

// --- contextual actions ------------------------------------------------------

/** Actions provided by each tab's mounted editor. */
export const tabActions = new SvelteMap<string, ToolbarAction[]>();

/**
 * To call when initializing an editor component: publishes its actions
 * for the toolbar (recomputed when the state read by `fn` changes).
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

/** Runs the tab's `id` action (keyboard shortcuts). Returns false if absent. */
export function runTabAction(tabIdValue: string, id: string): boolean {
  const a = tabActions.get(tabIdValue)?.find((x) => x.id === id);
  if (!a || a.disabled) return false;
  void a.run();
  return true;
}

// --- field highlighting ---------------------------------------------------

export const revealState = $state({ tabId: '', path: '', seq: 0 });

export function requestReveal(tabId: string, path: string): void {
  revealState.tabId = tabId;
  revealState.path = path;
  revealState.seq += 1;
}

/** Scrolls into view (and focuses) the `[data-path]` element closest to the path. */
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
 * To call within an editor: processes the highlight requests concerning it
 * (after render).
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

// --- selection ---------------------------------------------------------------------

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

// --- cross-cutting requests -------------------------------------------------------------

/** Counter incremented to request intent focus in "Test". */
export const focusRequests = $state({ tester: 0, search: 0 });
