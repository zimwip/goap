// An adapter definition: node `ADD:<name>` of the platform namespace. It implements the tools of one MCP
// with the operations of one connector; each organisational unit instantiates it (Adapter node) with its own values.
import type { AlgorithmParam, GraphNode, Struct } from './api';

export const NS_PLATFORM = 'platform';
export const ADAPTER_DEF_TYPE = 'AdapterDef';
export const adapterDefKey = (name: string) => `ADD:${name}`;

export interface AdapterDef {
  name: string;
  description: string;
  mcp: string;
  connector: string;
  language: string;
  code: string;
  params: AlgorithmParam[];
}

export function adapterDefFromNode(n: GraphNode): AdapterDef {
  const p = (n.props ?? {}) as Record<string, unknown>;
  const params = Array.isArray(p.params) ? (p.params as Record<string, unknown>[]) : [];
  return {
    name: String(p.name ?? ''),
    description: String(p.description ?? ''),
    mcp: String(p.mcp ?? ''),
    connector: String(p.connector ?? ''),
    language: String(p.language ?? 'javascript'),
    code: String(p.code ?? ''),
    params: params.map((q) => ({
      name: String(q.name ?? ''),
      type: String(q.type ?? 'string'),
      description: String(q.description ?? ''),
      required: !!q.required,
      defaultValue: q.default,
      values: Array.isArray(q.values) ? (q.values as string[]) : undefined,
    })),
  };
}

/** properties of the node (empty optional fields are left out) */
export function adapterDefProps(d: AdapterDef): Struct {
  return {
    name: d.name,
    description: d.description,
    mcp: d.mcp,
    connector: d.connector,
    language: d.language,
    code: d.code,
    params: d.params.map((p) => ({
      name: p.name ?? '',
      type: p.type ?? 'string',
      ...(p.description ? { description: p.description } : {}),
      ...(p.required ? { required: true } : {}),
      ...(p.defaultValue !== undefined && p.defaultValue !== null ? { default: p.defaultValue } : {}),
      ...(p.values?.length ? { values: p.values } : {}),
    })) as unknown as Struct[],
  };
}
