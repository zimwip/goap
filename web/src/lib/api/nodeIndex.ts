import { rpc } from './transport';
import type { NodeSearchRequest, NodeSearchResult } from './types/nodeIndex';

const INDEX = 'goap.index.v1.IndexService';

export const nodeIndex = {
  search: (req: NodeSearchRequest, signal?: AbortSignal) => rpc<NodeSearchRequest, NodeSearchResult>(INDEX, 'Search', req, signal),
  reindex: () => rpc<object, { versions?: number }>(INDEX, 'Reindex', {}),
  status: () =>
    rpc<object, { semantic?: boolean; store?: string; nodesIndexed?: string; baselinesFollowed?: string; errors?: string }>(INDEX, 'Status', {}),
};
