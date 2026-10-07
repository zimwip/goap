// Document index types (ADR 0026, 0095): lexical, semantic and hybrid search over the nodes and the changes of the
// graph, filtered by type, project, owner, status and methodology, with facets and nearest-document search. JSON of the
// proto3 messages of goap.index.v1 (camelCase, zero values omitted).

export type DocumentKind = 'node' | 'change';
export type SearchMode = 'lexical' | 'semantic' | 'hybrid';

/** What a hit of kind `change` adds. */
export interface ChangeInfo {
  changeId: string;
  title?: string;
  status?: string;
  projectId?: string;
  methodology?: string;
  ownerOrg?: string;
  parentId?: string;
}

/** A document of the index: a node version, or a change (kind `change`: id is the change id, version 0, no type). */
export interface NodeHit {
  kind?: DocumentKind;
  id: string;
  version: number;
  namespace: string;
  type: string;
  /** The key of the node, the change id for a change. */
  key: string;
  state?: string;
  branch: string;
  main?: boolean;
  deleted?: boolean;
  /** Key of the project of the node (the one it was created in), or of the change. */
  project?: string;
  /** Key of the unit owning the node version, or holding the change. */
  ownerUnit?: string;
  facets?: Record<string, string>;
  /** Fused rank in a hybrid search, cosine in a semantic one or similar_to. */
  score?: number;
  /** Cosine similarity when the hit came out of the vector side, else absent. */
  similarity?: number;
  /** Best matching passage (at most 240 characters), when asked. */
  snippet?: string;
  /** Set for a change. */
  change?: ChangeInfo;
}

/** The document a similar_to search starts from. */
export interface DocumentRef {
  kind: DocumentKind;
  id: string;
}

export interface NodeSearchRequest {
  /** Free text; empty: list by key. */
  text?: string;
  /** Default: node. Several kinds are searched together. */
  kinds?: DocumentKind[];
  /** Qualified node types ("alm@Requirement"); a change has none. */
  types?: string[];
  namespaces?: string[];
  /** Lifecycle state of the node, or of the change. */
  states?: string[];
  branches?: string[];
  /** true: heads of main only; false: not on main; absent: every branch. */
  main?: boolean;
  facetFilters?: { name: string; values: string[] }[];
  /** Facets to count: kind, namespace, type, state, branch, main, project, owner, status, methodology, or a declared one. */
  facets?: string[];
  /** Page size (the k of a nearest-neighbour search), default 20. */
  limit?: number;
  offset?: number;
  /** Project keys of the node or of the change. */
  projects?: string[];
  /** Also the projects below the given ones, resolved by the server. */
  includeSubprojects?: boolean;
  ownerUnits?: string[];
  /** Change statuses: setting it leaves no node. */
  statuses?: string[];
  /** Methodology names of changes: setting it leaves no node. */
  methodologies?: string[];
  /** Changes with no parent: setting it leaves no node. */
  rootsOnly?: boolean;
  /** Default hybrid. Semantic fails (failed precondition) without an embedding model; hybrid degrades to lexical. */
  mode?: SearchMode;
  /** Cosine floor of the vector matches, 0..1, replacing the service default for this request. */
  minSimilarity?: number;
  /** Return the best matching passage of each hit. */
  snippet?: boolean;
  /** The documents nearest to this one by its stored embedding; it is excluded, the filters hold. */
  similarTo?: DocumentRef;
}

export interface NodeSearchResult {
  hits?: NodeHit[];
  total?: number;
  facets?: { name: string; counts?: { value: string; count: number }[] }[];
  /** The embedding side took part in the ranking. */
  semantic?: boolean;
  truncated?: boolean;
}

export interface IndexStatus {
  semantic?: boolean;
  store?: string;
  nodesIndexed?: string;
  baselinesFollowed?: string;
  changesIndexed?: string;
  errors?: string;
}
