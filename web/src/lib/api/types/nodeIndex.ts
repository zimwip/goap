// Node index types (ADR 0026): hybrid full-text / semantic search with facets.

export interface NodeHit {
  id: string;
  version: number;
  namespace: string;
  type: string;
  key: string;
  state?: string;
  branch: string;
  main?: boolean;
  facets?: Record<string, string>;
  score?: number;
}

export interface NodeSearchRequest {
  text?: string;
  namespaces?: string[];
  types?: string[];
  states?: string[];
  branches?: string[];
  /** true: heads of main only; false: not on main; absent: every branch. */
  main?: boolean;
  facetFilters?: { name: string; values: string[] }[];
  /** Facets to count: namespace, type, state, branch, main, or a facet the node types declare. */
  facets?: string[];
  limit?: number;
  offset?: number;
}

export interface NodeSearchResult {
  hits?: NodeHit[];
  total?: number;
  facets?: { name: string; counts?: { value: string; count: number }[] }[];
  /** The embedding side took part in the ranking. */
  semantic?: boolean;
  truncated?: boolean;
}
