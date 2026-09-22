// Navigation minimale synchronisée avec le fragment d'URL :
//   #processus[/<id>]  #changement[/<id>]  #referentiel[/<id>]
//   #methodologies[/<nom>/<version> | /new]  #acces

export type Tab = 'processus' | 'changement' | 'referentiel' | 'methodologies' | 'acces';

export const TABS: { id: Tab; label: string }[] = [
  { id: 'processus', label: 'Processus' },
  { id: 'changement', label: 'Changement' },
  { id: 'referentiel', label: 'Référentiel' },
  { id: 'methodologies', label: 'Méthodologies' },
  { id: 'acces', label: 'Accès' },
];

interface NavState {
  tab: Tab;
  /** Identifiant sélectionné dans l'onglet courant (processus, changement, référentiel, méthodologie). */
  id: string;
  /** Second segment facultatif (version d'une méthodologie). */
  sub: string;
}

function parse(hash: string): NavState {
  const [tab = '', id = '', sub = ''] = hash.replace(/^#\/?/, '').split('/');
  const known = TABS.some((t) => t.id === tab);
  return {
    tab: known ? (tab as Tab) : 'processus',
    id: known ? decodeURIComponent(id) : '',
    sub: known ? decodeURIComponent(sub) : '',
  };
}

export const nav: NavState = $state(parse(location.hash));

export function href(tab: Tab, id = '', sub = ''): string {
  let h = `#${tab}`;
  if (id) h += `/${encodeURIComponent(id)}`;
  if (id && sub) h += `/${encodeURIComponent(sub)}`;
  return h;
}

// --- garde de sortie ----------------------------------------------------------
// Un écran avec des modifications non enregistrées peut installer une garde :
// elle est consultée avant toute navigation interne et renvoie false pour
// l'annuler (typiquement après un `confirm()`).

type LeaveGuard = () => boolean;
let guard: LeaveGuard | null = null;

/** Installe (ou retire avec null) la garde de sortie. Renvoie une fonction de retrait. */
export function setLeaveGuard(fn: LeaveGuard | null): () => void {
  guard = fn;
  return () => {
    if (guard === fn) guard = null;
  };
}

function same(a: NavState, b: NavState): boolean {
  return a.tab === b.tab && a.id === b.id && a.sub === b.sub;
}

export function go(tab: Tab, id = '', sub = ''): void {
  const next: NavState = { tab, id, sub: id ? sub : '' };
  if (!same(next, nav) && guard && !guard()) return;
  const hash = href(tab, id, sub);
  nav.tab = next.tab;
  nav.id = next.id;
  nav.sub = next.sub;
  if (location.hash !== hash) location.hash = hash;
}

window.addEventListener('hashchange', () => {
  const next = parse(location.hash);
  if (same(next, nav)) return;
  if (guard && !guard()) {
    // Annulation : on remet le fragment courant sans créer d'entrée d'historique.
    history.replaceState(null, '', href(nav.tab, nav.id, nav.sub));
    return;
  }
  nav.tab = next.tab;
  nav.id = next.id;
  nav.sub = next.sub;
});
