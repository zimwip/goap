// MCP hub types: connectors (registry), MCPs and adapters (graph nodes, read here).

import type { Struct } from './common';

export interface ConnectorOperation {
  name?: string;
  description?: string;
  inputSchema?: Struct;
}

export interface ConnectorInfo {
  id?: string;
  version?: string;
  description?: string;
  /** JSON Schema of the parameters an adapter gives the connector */
  configSchema?: Struct;
  secretNames?: string[];
  operations?: ConnectorOperation[];
}

export interface Connector {
  info?: ConnectorInfo;
  endpoint?: string;
  lastSeen?: string;
  live?: boolean;
}

export interface McpTool {
  name?: string;
  description?: string;
  inputSchema?: Struct;
  /** the tool changes nothing (a unit can keep only these) */
  readOnly?: boolean;
}

/** Where a methodology may use an MCP (ADR 0028): declared by actions, by agents only, or both. */
export type McpScope = 'action' | 'agent' | 'both';

/** An MCP: the generic usage of a tool by an LLM (node `MCP:<name>` of the platform namespace). */
export interface Mcp {
  name?: string;
  description?: string;
  tools?: McpTool[];
  /** empty: both */
  scope?: McpScope;
}

/**
 * An organisational unit's instance of an adapter of the library (an algorithm of type `adapter`): node
 * `ADP:<unit>/<mcp>` of the organisation namespace, owned by the unit. The connector and the code come from
 * the algorithm; the unit gives the parameter values (secrets as references).
 */
export interface Adapter {
  unit?: string;
  mcp?: string;
  /** name of the adapter definition (node `ADD:<name>` of the platform namespace) */
  adapter?: string;
  params?: Struct;
  /**
   * Restrictions of the MCP for the unit and its sub-units (ADR 0028); they add up along the unit chain. An
   * instance without `adapter` only restricts (the implementation is inherited).
   */
  disabled?: boolean;
  /** when not empty, the only tools allowed */
  tools?: string[];
  /** tools refused */
  deny?: string[];
  /** only the read-only tools */
  readOnly?: boolean;
}

export interface EffectiveMcp {
  mcp?: Mcp;
  adapter?: Adapter;
  inherited?: boolean;
  /** the connector the adapter calls (empty when the adapter definition cannot say) */
  connector?: string;
  /** the tools the unit may call once the restrictions of its chain apply */
  allowedTools?: string[];
  /** the units whose instance restricts the MCP, nearest first */
  restrictedBy?: string[];
  disabled?: boolean;
  /** built into the platform (goap-graph, goap-change, goap-scheduler, goap-admin) */
  builtin?: boolean;
}

export interface TemplateParam {
  name?: string;
  type?: string;
  description?: string;
  required?: boolean;
}

export interface AdapterTemplate {
  code?: string;
  params?: TemplateParam[];
}

export interface HubTool {
  name?: string;
  description?: string;
  inputSchema?: Struct;
}
