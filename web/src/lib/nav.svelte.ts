// Navigation minimale synchronisée avec le fragment d'URL :
//   #processus[/<id>]  #changement[/<id>]  #referentiel[/<id>]  #methodologies

export type Tab = 'processus' | 'changement' | 'referentiel' | 'methodologies';

export const TABS: { id: Tab; label: string }[] = [
  { id: 'processus', label: 'Processus' },
  { id: 'changement', label: 'Changement' },
  { id: 'referentiel', label: 'Référentiel' },
  { id: 'methodologies', label: 'Méthodologies' },
];

interface NavState {
  tab: Tab;
  /** Identifiant sélectionné dans l'onglet courant (processus, changement, référentiel). */
  id: string;
}

function parse(hash: string): NavState {
  const [tab = '', id = ''] = hash.replace(/^#\/?/, '').split('/');
  const known = TABS.some((t) => t.id === tab);
  return { tab: known ? (tab as Tab) : 'processus', id: known ? decodeURIComponent(id) : '' };
}

export const nav: NavState = $state(parse(location.hash));

export function go(tab: Tab, id = ''): void {
  const hash = `#${tab}${id ? `/${encodeURIComponent(id)}` : ''}`;
  if (location.hash !== hash) location.hash = hash;
  nav.tab = tab;
  nav.id = id;
}

export function href(tab: Tab, id = ''): string {
  return `#${tab}${id ? `/${encodeURIComponent(id)}` : ''}`;
}

window.addEventListener('hashchange', () => {
  const next = parse(location.hash);
  nav.tab = next.tab;
  nav.id = next.id;
});
