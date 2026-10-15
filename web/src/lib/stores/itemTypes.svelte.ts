// The item types (ADR 0119): the kinds of item a change may hold, loaded once from the graph (they change with the
// platform code only).
import { graph, errorMessage, type ItemKindInfo } from '../api';

interface ItemTypes {
  items: ItemKindInfo[];
  loading: boolean;
  loaded: boolean;
  error: string;
}

export const itemTypes: ItemTypes = $state({ items: [], loading: false, loaded: false, error: '' });

export async function refreshItemTypes(): Promise<void> {
  itemTypes.loading = true;
  try {
    itemTypes.items = (await graph.listItemKinds()).kinds ?? [];
    itemTypes.error = '';
  } catch (e) {
    itemTypes.error = errorMessage(e);
  } finally {
    itemTypes.loading = false;
    itemTypes.loaded = true;
  }
}

export const itemTypeOf = (kind: string): ItemKindInfo | undefined => itemTypes.items.find((k) => k.kind === kind);
