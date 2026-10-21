// The context bar's state: what the editor shown tells it (its sections, as icons with their counts) and whether it
// is open while not pinned (revealed). Pinned is `layout.rightOpen`.
import type { Pane } from '../components/EditorPanes.svelte';
import type { IconName } from './Icon.svelte';

export interface EditorContext {
  /** who published it: only its owner takes it back */
  owner: symbol;
  label: string;
  panes: Pane[];
  active: string;
  pick: (id: string) => void;
  /** the change the sections are of, when the editor is a change's */
  changeId?: string;
}

export const editorContext = $state({ current: undefined as EditorContext | undefined });

/** The bar opened over the editor while not pinned (hovered, focused, or asked for), and whether it is docked. */
export const contextBar = $state({
  revealed: false,
  /** pinned on a wide screen: the bar, open, takes its place beside the editor */
  docked: false,
});

export function publishContext(c: EditorContext): void {
  editorContext.current = c;
}

export function withdrawContext(owner: symbol): void {
  if (editorContext.current?.owner === owner) editorContext.current = undefined;
}

// the icon of a section by its id, shared by the editors (a pane may name its own)
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

export const paneIcon = (p: Pane): IconName => p.icon ?? PANE_ICONS[p.id] ?? 'list';

/** What the icon of a closed bar shows of a badge: its number, a mark, or nothing. */
export function compactBadge(badge: string | number | undefined): string {
  if (badge === undefined || badge === '') return '';
  if (typeof badge === 'number') return badge > 99 ? '99+' : String(badge);
  const n = /^\d+/.exec(badge)?.[0];
  if (n) return Number(n) > 99 ? '99+' : n;
  return badge === '!' ? badge : '';
}
