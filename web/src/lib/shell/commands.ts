// Global commands (search's ">" palette and keyboard shortcuts).
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
    label: 'Save active editor',
    icon: 'save',
    shortcut: 'Ctrl+S',
    run: () => {
      const t = activeTab();
      if (t) runTabAction(t.id, 'save');
    },
  },
  {
    id: 'close',
    label: 'Close tab',
    icon: 'x',
    shortcut: 'Ctrl+W / Alt+W',
    run: () => {
      const t = activeTab();
      if (t) closeTab(t.id);
    },
  },
  { id: 'closeAll', label: 'Close all tabs', icon: 'x', run: closeAll },
  { id: 'console', label: 'Show / hide console', icon: 'panelBottom', shortcut: 'Ctrl+J', run: toggleConsole },
  {
    id: 'nav',
    label: 'Show / hide navigation',
    icon: 'panelLeft',
    shortcut: 'Ctrl+B',
    run: () => (layout.leftOpen = !layout.leftOpen),
  },
  {
    id: 'test',
    label: 'New intent test',
    icon: 'flask',
    run: () => {
      showTool('right', 'tester');
      focusRequests.tester += 1;
    },
  },
  {
    id: 'newMethodology',
    label: 'New methodology',
    icon: 'plus',
    run: () => openTab({ kind: 'methodology', params: { name: '', version: '' } }, { pin: true }),
  },
  { id: 'tokenUsage', label: 'Token usage dashboard', icon: 'coins', run: () => openTab({ kind: 'tokenUsage', params: {} }, { pin: true }) },
  { id: 'import', label: 'Import a methodology (YAML)', icon: 'upload', run: () => openTab({ kind: 'import', params: {} }, { pin: true }) },
  { id: 'policies', label: 'Open access policies', icon: 'shield', run: () => openTab({ kind: 'policies', params: {} }) },
  { id: 'dsl', label: 'DSL Help', icon: 'help', run: () => showTool('right', 'dsl') },
  { id: 'assistant', label: 'Open assistant', icon: 'chat', run: () => openTab({ kind: 'assistant', params: {} }, { pin: true }) },
  { id: 'triggers', label: 'Triggers', icon: 'clock', run: () => showTool('left', 'triggers') },
  { id: 'themeLight', label: 'Theme: light', icon: 'moon', run: () => (layout.theme = 'light') },
  { id: 'themeDark', label: 'Theme: dark', icon: 'moon', run: () => (layout.theme = 'dark') },
  { id: 'themeAuto', label: 'Theme: system', icon: 'moon', run: () => (layout.theme = 'auto') },
];

export function runCommand(id: string): void {
  COMMANDS.find((c) => c.id === id)?.run();
}
