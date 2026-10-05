import { rpc } from './transport';
import type { Empty } from './types/common';
import type { Adapter, AdapterTemplate, Connector, EffectiveMcp, HubTool, Mcp } from './types/mcp';

const MCP = 'goap.mcp.v1.McpService';

export const mcp = {
  listConnectors: (signal?: AbortSignal) => rpc<Empty, { connectors?: Connector[] }>(MCP, 'ListConnectors', {}, signal),
  listMcps: (signal?: AbortSignal) => rpc<Empty, { mcps?: Mcp[] }>(MCP, 'ListMcps', {}, signal),
  /** the MCPs a unit can use, each with the adapter that implements it (own or inherited); chain: unit then ancestors */
  listEffective: (unit: string, signal?: AbortSignal) =>
    rpc<{ unit: string }, { chain?: string[]; mcps?: EffectiveMcp[] }>(MCP, 'ListEffective', { unit }, signal),
  /** blocking problems come back as errors, the rest as warnings */
  checkAdapter: (adapter: Adapter) => rpc<{ adapter: Adapter }, { warnings?: string[] }>(MCP, 'CheckAdapter', { adapter }),
  /** skeleton of the code of an adapter between an MCP and a registered connector, and the parameters the connector needs */
  adapterTemplate: (mcpName: string, connector: string) =>
    rpc<{ mcp: string; connector: string }, AdapterTemplate>(MCP, 'AdapterTemplate', { mcp: mcpName, connector }),
  listTools: (unit = '', signal?: AbortSignal) =>
    rpc<{ unit: string }, { tools?: HubTool[]; mcps?: string[] }>(MCP, 'ListTools', { unit }, signal),
};
