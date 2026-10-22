// The icon of an editor's section (a tab of `EditorPanes`) by its id, shared by the editors; a pane may name its own.
import type { IconName } from '../shell/Icon.svelte';

const PANE_ICONS: Record<string, IconName> = {
  overview: 'app',
  details: 'list',
  impacts: 'diff',
  reviews: 'check',
  artifacts: 'file',
  decisions: 'help',
  risks: 'alert',
  verification: 'shield',
  derogations: 'key',
  changes: 'branch',
  audit: 'trace',
  graph: 'graph',
  issues: 'alert',
  save: 'save',
  types: 'database',
  relations: 'node',
  lifecycle: 'refresh',
  history: 'clock',
  mcp: 'zap',
  members: 'user',
  assignments: 'tag',
};

export const paneIcon = (p: { id: string; icon?: IconName }): IconName => p.icon ?? PANE_ICONS[p.id] ?? 'list';
