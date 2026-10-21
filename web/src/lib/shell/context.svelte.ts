// The context bar's state: what the editor shown tells it (its sections, what waits on people) and whether it is
// shown while not pinned (revealed). Pinned is `layout.rightOpen`; the tool of its lower part is `layout.right`.
import type { Snippet } from 'svelte';
import type { Pane } from '../components/EditorPanes.svelte';

export interface EditorContext {
  /** who published it: only its owner takes it back */
  owner: symbol;
  label: string;
  panes: Pane[];
  active: string;
  pick: (id: string) => void;
  /** the change the sections are of, when the editor is a change's */
  changeId?: string;
  /** what the editor puts under its sections (what waits on people…) */
  aside?: Snippet;
}

export const editorContext = $state({ current: undefined as EditorContext | undefined });

/** The bar shown over the editor while not pinned (hovered, focused, or asked for), and whether it is docked. */
export const contextBar = $state({
  revealed: false,
  /** pinned on a wide screen: the bar takes its place beside the editor, which no longer repeats its sections */
  docked: false,
});

export function publishContext(c: EditorContext): void {
  editorContext.current = c;
}

export function withdrawContext(owner: symbol): void {
  if (editorContext.current?.owner === owner) editorContext.current = undefined;
}

/** The sections of a change, for a change the editor does not show (they open its tab on that section). */
export const CHANGE_SECTIONS: { id: string; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'impacts', label: 'Impacts' },
  { id: 'reviews', label: 'Reviews' },
  { id: 'artifacts', label: 'Artifacts' },
  { id: 'decisions', label: 'Decisions' },
  { id: 'risks', label: 'Risks & actions' },
  { id: 'verification', label: 'Verification' },
  { id: 'derogations', label: 'Derogations' },
  { id: 'changes', label: 'Changes' },
  { id: 'audit', label: 'Audit' },
];
