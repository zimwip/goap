// Commandes globales (palette « > » de la recherche et raccourcis clavier).
import type { IconName } from './Icon.svelte';
import { layout, showTool, toggleConsole } from './layout.svelte';
import { activeTab, closeAll, closeTab, openTab } from './tabs.svelte';
import { focusRequests, runTabAction } from './workbench.svelte';

export interface Command {
  id: string;
  label: string;
  icon?: IconName;
  shortcut?: string;
  run: () => void;
}

export const COMMANDS: Command[] = [
  {
    id: 'save',
    label: "Enregistrer l'éditeur actif",
    icon: 'save',
    shortcut: 'Ctrl+S',
    run: () => {
      const t = activeTab();
      if (t) runTabAction(t.id, 'save');
    },
  },
  {
    id: 'close',
    label: "Fermer l'onglet",
    icon: 'x',
    shortcut: 'Ctrl+W / Alt+W',
    run: () => {
      const t = activeTab();
      if (t) closeTab(t.id);
    },
  },
  { id: 'closeAll', label: 'Fermer tous les onglets', icon: 'x', run: closeAll },
  { id: 'console', label: 'Afficher / masquer la console', icon: 'panelBottom', shortcut: 'Ctrl+J', run: toggleConsole },
  {
    id: 'nav',
    label: 'Afficher / masquer la navigation',
    icon: 'panelLeft',
    shortcut: 'Ctrl+B',
    run: () => (layout.leftOpen = !layout.leftOpen),
  },
  {
    id: 'test',
    label: "Nouveau test d'intention",
    icon: 'flask',
    run: () => {
      showTool('right', 'tester');
      focusRequests.tester += 1;
    },
  },
  {
    id: 'newMethodology',
    label: 'Nouvelle méthodologie',
    icon: 'plus',
    run: () => openTab({ kind: 'methodology', params: { name: '', version: '' } }, { pin: true }),
  },
  { id: 'import', label: 'Importer une méthodologie (YAML)', icon: 'upload', run: () => openTab({ kind: 'import', params: {} }, { pin: true }) },
  { id: 'policies', label: 'Ouvrir les politiques d’accès', icon: 'shield', run: () => openTab({ kind: 'policies', params: {} }) },
  { id: 'dsl', label: 'Aide DSL', icon: 'help', run: () => showTool('right', 'dsl') },
  { id: 'assistant', label: 'Ouvrir l’assistant', icon: 'chat', run: () => openTab({ kind: 'assistant', params: {} }, { pin: true }) },
  { id: 'triggers', label: 'Déclencheurs', icon: 'clock', run: () => showTool('left', 'triggers') },
  { id: 'themeLight', label: 'Thème : clair', icon: 'moon', run: () => (layout.theme = 'light') },
  { id: 'themeDark', label: 'Thème : sombre', icon: 'moon', run: () => (layout.theme = 'dark') },
  { id: 'themeAuto', label: 'Thème : système', icon: 'moon', run: () => (layout.theme = 'auto') },
];

export function runCommand(id: string): void {
  COMMANDS.find((c) => c.id === id)?.run();
}
