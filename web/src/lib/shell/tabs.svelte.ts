// Onglets de la zone d'édition, avec le comportement des « onglets d'aperçu »
// de VS Code : un clic simple ouvre un aperçu (titre en italique) remplacé par
// la sélection suivante ; un onglet épinglé (double clic, icône, ou première
// modification) n'est jamais remplacé.
import { editorView } from './registry';
import { loadRaw, save } from './storage';
import type { Tab, TabSpec } from './types';

const KEY = 'goap.ide.tabs';

interface TabsState {
  tabs: Tab[];
  active: string;
}

function restore(): TabsState {
  const raw = loadRaw(KEY) as Partial<TabsState> | undefined;
  const tabs = Array.isArray(raw?.tabs)
    ? raw.tabs.filter(
        (t): t is Tab =>
          !!t && typeof t.id === 'string' && typeof t.kind === 'string' && typeof t.params === 'object' && !!t.params,
      )
    : [];
  const active = typeof raw?.active === 'string' && tabs.some((t) => t.id === raw.active) ? raw.active : (tabs[0]?.id ?? '');
  return { tabs, active };
}

export const tabsState: TabsState = $state(restore());

/** Historique d'activation (pour revenir à l'onglet précédent à la fermeture). */
const history: string[] = [];

$effect.root(() => {
  $effect(() => {
    save(KEY, {
      tabs: tabsState.tabs.map((t) => ({ id: t.id, kind: t.kind, params: { ...t.params }, pinned: t.pinned })),
      active: tabsState.active,
    });
  });
});

export function tabId(spec: TabSpec): string {
  const v = editorView(spec.kind);
  return `${spec.kind}:${v ? v.key(spec.params) : JSON.stringify(spec.params)}`;
}

export function activeTab(): Tab | undefined {
  return tabsState.tabs.find((t) => t.id === tabsState.active);
}

export function findTab(id: string): Tab | undefined {
  return tabsState.tabs.find((t) => t.id === id);
}

export function isDirty(tab: Tab): boolean {
  return editorView(tab.kind)?.dirty?.(tab) ?? false;
}

export function activate(id: string): void {
  if (!findTab(id)) return;
  if (tabsState.active && tabsState.active !== id) {
    const i = history.indexOf(tabsState.active);
    if (i >= 0) history.splice(i, 1);
    history.push(tabsState.active);
  }
  tabsState.active = id;
}

/**
 * Ouvre (ou active) l'onglet d'un objet. Sans `pin`, l'onglet est un aperçu
 * qui remplace l'aperçu courant, sauf si celui-ci a des modifications.
 */
export function openTab(spec: TabSpec, opts: { pin?: boolean; background?: boolean } = {}): Tab {
  const id = tabId(spec);
  const existing = findTab(id);
  if (existing) {
    if (opts.pin) existing.pinned = true;
    // Les paramètres peuvent porter des informations plus récentes (nom…).
    Object.assign(existing.params, spec.params);
    if (!opts.background) activate(id);
    return existing;
  }
  const tab: Tab = { id, kind: spec.kind, params: { ...spec.params }, pinned: !!opts.pin };
  const previewIdx = tabsState.tabs.findIndex((t) => !t.pinned);
  if (previewIdx >= 0 && isDirty(tabsState.tabs[previewIdx])) tabsState.tabs[previewIdx].pinned = true;
  const reuse = tabsState.tabs.findIndex((t) => !t.pinned);
  if (reuse >= 0) {
    forget(tabsState.tabs[reuse].id);
    tabsState.tabs[reuse] = tab;
  } else {
    const at = tabsState.tabs.findIndex((t) => t.id === tabsState.active);
    tabsState.tabs.splice(at < 0 ? tabsState.tabs.length : at + 1, 0, tab);
  }
  if (!opts.background || !tabsState.active) activate(id);
  return findTab(id) ?? tab;
}

export function pinTab(id: string): void {
  const t = findTab(id);
  if (t) t.pinned = true;
}

export function togglePin(id: string): void {
  const t = findTab(id);
  if (t) t.pinned = !t.pinned;
}

function forget(id: string): void {
  let i: number;
  while ((i = history.indexOf(id)) >= 0) history.splice(i, 1);
}

/**
 * Ferme un onglet. Si ses modifications ne sont partagées par aucun autre
 * onglet ouvert, demande confirmation puis les abandonne.
 */
export function closeTab(id: string, opts: { force?: boolean } = {}): boolean {
  const idx = tabsState.tabs.findIndex((t) => t.id === id);
  if (idx < 0) return false;
  const tab = tabsState.tabs[idx];
  const view = editorView(tab.kind);
  if (!opts.force && (view?.groupDirty?.(tab) ?? view?.dirty?.(tab))) {
    const group = view.group?.(tab);
    const shared =
      !!group && tabsState.tabs.some((t) => t.id !== id && editorView(t.kind)?.group?.(t) === group);
    if (!shared) {
      const title = view.tabTitle(tab);
      if (!confirm(`« ${title} » contient des modifications non enregistrées. Fermer et les abandonner ?`)) return false;
      view.discard?.(tab);
    }
  }
  tabsState.tabs.splice(idx, 1);
  forget(id);
  if (tabsState.active === id) {
    let next = '';
    while (history.length && !next) {
      const h = history.pop() ?? '';
      if (findTab(h)) next = h;
    }
    tabsState.active = next || tabsState.tabs[Math.min(idx, tabsState.tabs.length - 1)]?.id || '';
  }
  return true;
}

export function closeOthers(id: string): void {
  for (const t of [...tabsState.tabs]) if (t.id !== id) closeTab(t.id);
}

export function closeAll(): void {
  for (const t of [...tabsState.tabs]) closeTab(t.id);
}

/** Remplace un onglet par un autre objet (ex. nouvelle méthodologie enregistrée). */
export function replaceTab(id: string, spec: TabSpec): void {
  const idx = tabsState.tabs.findIndex((t) => t.id === id);
  const nid = tabId(spec);
  if (idx < 0) {
    openTab(spec, { pin: true });
    return;
  }
  const dup = tabsState.tabs.findIndex((t) => t.id === nid);
  if (dup >= 0 && dup !== idx) tabsState.tabs.splice(dup, 1);
  const i = tabsState.tabs.findIndex((t) => t.id === id);
  tabsState.tabs[i] = { id: nid, kind: spec.kind, params: { ...spec.params }, pinned: true };
  forget(id);
  if (tabsState.active === id) tabsState.active = nid;
}

/** Ferme sans confirmation les onglets dont l'objet n'existe plus. */
export function closeWhere(pred: (t: Tab) => boolean): void {
  for (const t of [...tabsState.tabs]) if (pred(t)) closeTab(t.id, { force: true });
}

export function moveTab(from: number, to: number): void {
  if (from === to || from < 0 || to < 0 || from >= tabsState.tabs.length || to >= tabsState.tabs.length) return;
  const [t] = tabsState.tabs.splice(from, 1);
  tabsState.tabs.splice(to, 0, t);
}
