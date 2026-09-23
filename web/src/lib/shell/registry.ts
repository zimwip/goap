// View registry: each IDE zone displays the views registered for it
// (activity bar tools, console tabs, editors).
import type { EditorView, PanelView, View, Zone } from './types';

const views = new Map<string, View>();

export function registerView(v: View): void {
  views.set(v.zone === 'editor' ? `editor:${v.id}` : v.id, v);
}

export function viewsIn(zone: 'left' | 'right' | 'bottom'): PanelView[] {
  return [...views.values()]
    .filter((v): v is PanelView => v.zone === zone)
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
}

export function panelView(id: string): PanelView | undefined {
  const v = views.get(id);
  return v && v.zone !== 'editor' ? v : undefined;
}

export function editorView(kind: string): EditorView | undefined {
  const v = views.get(`editor:${kind}`);
  return v?.zone === 'editor' ? v : undefined;
}

export function zoneOf(id: string): Zone | undefined {
  return views.get(id)?.zone;
}
