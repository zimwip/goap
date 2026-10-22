// A read query of the graph explorer (ADR 0160): its cells read back (a node, a link, a path or a plain value) and its
// nodes and links laid into the explorer as one neighbourhood whose nodes are not expanded yet.
import type { GraphNode, Link, QueryResult } from './api';
import type { Hood } from './graphIndex';

export type Cell =
  | { kind: 'node'; id: string; node?: GraphNode }
  | { kind: 'link'; id: string; link?: Link }
  | { kind: 'path'; nodes: string[]; links: string[] }
  | { kind: 'value'; value: unknown };

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Reads a cell of a row: the objects naming a node, a link or a path, else the value itself. */
export function cellOf(v: unknown, res: Pick<QueryResult, 'nodes' | 'links'> = {}): Cell {
  if (isObject(v)) {
    const keys = Object.keys(v);
    if (keys.length === 1 && typeof v.$node === 'string') return { kind: 'node', id: v.$node, node: res.nodes?.find((n) => n.id === v.$node) };
    if (keys.length === 1 && typeof v.$link === 'string') return { kind: 'link', id: v.$link, link: res.links?.find((l) => l.id === v.$link) };
    if (keys.length === 1 && isObject(v.$path)) {
      const p = v.$path;
      const ids = (x: unknown) => (Array.isArray(x) ? x.filter((i): i is string => typeof i === 'string') : []);
      return { kind: 'path', nodes: ids(p.nodes), links: ids(p.links) };
    }
  }
  return { kind: 'value', value: v };
}

/** The label of a node: its name, title or key. */
export function nodeLabel(n: GraphNode | undefined, fallback = ''): string {
  const p = (n?.props ?? {}) as Record<string, unknown>;
  return String(p.name ?? p.title ?? p.displayName ?? n?.key ?? fallback);
}

/** A plain value as one line of text: strings as they are, the rest as JSON; the entities a list holds by their labels. */
export function valueText(v: unknown, res: Pick<QueryResult, 'nodes' | 'links'> = {}): string {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  const c = cellOf(v, res);
  switch (c.kind) {
    case 'node':
      return nodeLabel(c.node, c.id);
    case 'link':
      return c.link?.type ?? c.id;
    case 'path':
      return `path of ${c.links.length} link${c.links.length === 1 ? '' : 's'}`;
  }
  if (Array.isArray(v)) return `[${v.map((x) => valueText(x, res)).join(', ')}]`;
  return JSON.stringify(v);
}

/** The node ids a cell names (a node, the ends of a link, the nodes of a path), to select them in the canvas. */
export function cellNodes(c: Cell): string[] {
  switch (c.kind) {
    case 'node':
      return [c.id];
    case 'link':
      return [c.link?.from?.id ?? '', c.link?.to?.id ?? ''].filter(Boolean);
    case 'path':
      return c.nodes;
  }
  return [];
}

/** The answer of a query as a neighbourhood of the explorer: its nodes and links, none of them expanded. */
export function queryHood(res: QueryResult): Hood {
  return { nodes: res.nodes ?? [], links: res.links ?? [] };
}

/** Whether a node matches the filter of a node's Graph pane: a part of its key, label or type, ignoring case. */
export function matchesFilter(n: GraphNode, text: string): boolean {
  const t = text.trim().toLowerCase();
  return !t || [n.key, nodeLabel(n), n.type].some((s) => (s ?? '').toLowerCase().includes(t));
}

/** One line saying what a query read and found, and why it stopped. */
export function querySummary(res: QueryResult): string {
  const rows = res.rows?.length ?? 0;
  const parts = [`${rows} row${rows === 1 ? '' : 's'}`, `${res.nodes?.length ?? 0} nodes`, `${res.links?.length ?? 0} links`, `${res.reads ?? 0} reads`];
  if (res.truncated) parts.push('stopped by a cap');
  return parts.join(' · ');
}

/** Example queries of the explorer: one per shape the language reads. */
export const QUERY_EXAMPLES: { label: string; query: string }[] = [
  { label: 'Nodes of a type', query: 'MATCH (r:Requirement)\nRETURN r.key, r.title\nORDER BY r.key\nLIMIT 50' },
  { label: 'From a key, two hops', query: "MATCH p = (n {key: 'REQ-1'})-[*1..2]-(x)\nRETURN p" },
  { label: 'Filter on attributes', query: "MATCH (r:Requirement)-[s:satisfies]->(n:Need)\nWHERE n.title CONTAINS 'pay' AND r.state <> 'retired'\nRETURN r, s, n" },
  { label: 'Count by type', query: 'MATCH (r:Requirement)<-[l]-(x)\nRETURN type(l) AS link, count(*) AS n\nORDER BY n DESC' },
  { label: 'Without a link', query: 'MATCH (r:Requirement)\nWHERE NOT EXISTS { (r)<-[:verifies]-(:TestCase) }\nRETURN r' },
  { label: 'Optional match', query: 'MATCH (r:Requirement)\nOPTIONAL MATCH (r)<-[:verifies]-(t)\nRETURN r.key, collect(t.key) AS tests' },
];

/** Example queries of a node's Graph pane: each starts from the node, `n`. */
export const NODE_QUERY_EXAMPLES: { label: string; query: string }[] = [
  { label: 'Its neighbours', query: 'MATCH (n)-[l]-(m)\nRETURN m' },
  { label: 'Two hops, one type', query: 'MATCH p = (n)-[*1..2]-(m:Requirement)\nRETURN p' },
  { label: 'Filter on attributes', query: "MATCH (n)-[l]-(m)\nWHERE m.state <> 'retired' AND m.title CONTAINS 'pay'\nRETURN m" },
  { label: 'Neighbours without a link', query: 'MATCH (n)--(m)\nWHERE NOT (m)<-[:verifies]-()\nRETURN m' },
  { label: 'Optional match', query: 'MATCH (n)--(m)\nOPTIONAL MATCH (m)<-[:verifies]-(t)\nRETURN m, collect(t.key) AS tests' },
];
