import { rpc } from './transport';
import type { IndexStatus, NodeSearchRequest, NodeSearchResult } from './types/nodeIndex';

const INDEX = 'goap.index.v1.IndexService';

export const nodeIndex = {
  search: (req: NodeSearchRequest, signal?: AbortSignal) => rpc<NodeSearchRequest, NodeSearchResult>(INDEX, 'Search', req, signal),
  reindex: () => rpc<object, { versions?: number }>(INDEX, 'Reindex', {}),
  status: () => rpc<object, IndexStatus>(INDEX, 'Status', {}),
  /** The changes nearest to an existing change (its stored embedding, no embedding call); it is not in the answer. */
  similarChanges: (changeId: string, req: NodeSearchRequest = {}, signal?: AbortSignal) =>
    rpc<NodeSearchRequest, NodeSearchResult>(INDEX, 'Search', { ...req, kinds: ['change'], similarTo: { kind: 'change', id: changeId } }, signal),
};
