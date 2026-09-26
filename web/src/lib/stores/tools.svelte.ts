// State of the "Connectors" and "MCPs" explorers: the registry of connectors (hub) and the MCPs (graph nodes, read through the hub).
import { mcp, errorMessage, type Connector, type Mcp } from '../api';

export const tools = $state({
  connectors: [] as Connector[],
  mcps: [] as Mcp[],
  loading: false,
  loaded: false,
  error: '',
});

export async function refreshTools(): Promise<void> {
  tools.loading = true;
  try {
    const [c, m] = await Promise.all([mcp.listConnectors(), mcp.listMcps()]);
    tools.connectors = c.connectors ?? [];
    tools.mcps = m.mcps ?? [];
    tools.error = '';
  } catch (e) {
    tools.error = errorMessage(e);
  } finally {
    tools.loading = false;
    tools.loaded = true;
  }
}
