// The item types (ADR 0119) a change may hold, loaded once from the graph (they change with the
// platform code only).
import { graph, errorMessage, type ItemTypeInfo } from '../api';

interface ItemTypes {
  items: ItemTypeInfo[];
  loading: boolean;
  loaded: boolean;
  error: string;
}

export const itemTypes: ItemTypes = $state({ items: [], loading: false, loaded: false, error: '' });

export async function refreshItemTypes(): Promise<void> {
  itemTypes.loading = true;
  try {
    itemTypes.items = (await graph.listItemTypes()).types ?? [];
    itemTypes.error = '';
  } catch (e) {
    itemTypes.error = errorMessage(e);
  } finally {
    itemTypes.loading = false;
    itemTypes.loaded = true;
  }
}

export const itemTypeOf = (type: string): ItemTypeInfo | undefined => itemTypes.items.find((k) => k.type === type);
