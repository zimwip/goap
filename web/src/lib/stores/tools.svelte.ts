// State of the "Connectors" and "MCPs" explorers and of the adapter library: the registry of connectors (hub), the MCPs
// (graph nodes, read through the hub) and the adapter definitions (graph nodes).
import { mcp, errorMessage, type Connector, type Mcp } from '../api';
import { headGraph } from '../graphEdit';
import { ADAPTER_DEF_TYPE, NS_PLATFORM, adapterDefFromNode, type AdapterDef } from '../adapterDef';

/** Adapter definitions are graph nodes of the platform namespace (see adapterDef.ts). */
export const tools = $state({
  connectors: [] as Connector[],
  mcps: [] as Mcp[],
  adapterDefs: [] as AdapterDef[],
  loading: false,
  loaded: false,
  error: '',
});

async function loadAdapterDefs(): Promise<AdapterDef[]> {
  let nodes;
  try {
    nodes = (await headGraph(NS_PLATFORM)).nodes;
  } catch {
    return []; // no baseline yet
  }
  return nodes
    .filter((n) => n.namespace === NS_PLATFORM && n.type === ADAPTER_DEF_TYPE && !n.deleted)
    .map(adapterDefFromNode)
    .sort((x, y) => x.name.localeCompare(y.name));
}

export async function refreshTools(): Promise<void> {
  tools.loading = true;
  try {
    const [c, m, lib] = await Promise.all([mcp.listConnectors(), mcp.listMcps(), loadAdapterDefs()]);
    tools.connectors = c.connectors ?? [];
    tools.mcps = m.mcps ?? [];
    tools.adapterDefs = lib;
    tools.error = '';
  } catch (e) {
    tools.error = errorMessage(e);
  } finally {
    tools.loading = false;
    tools.loaded = true;
  }
}
