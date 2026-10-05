// State of the "Connectors" and "MCPs" explorers and of the adapter library: the registry of connectors (hub), the MCPs
// (graph nodes, read through the hub) and the adapter definitions (graph nodes).
import { mcp, errorMessage, type Connector, type Mcp } from '../api';
import { headGraph } from '../graphEdit';
import { ns } from './session.svelte';
import { ADAPTER_DEF_TYPE, adapterDefFromNode, type AdapterDef } from '../adapterDef';

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
    nodes = (await headGraph(ns.platform)).nodes;
  } catch {
    return []; // no baseline yet
  }
  return nodes
    .filter((n) => n.namespace === ns.platform && n.type === ADAPTER_DEF_TYPE && !n.deleted)
    .map(adapterDefFromNode)
    .sort((x, y) => x.name.localeCompare(y.name));
}

/** The MCPs of a scope (ADR 0028): the ones only agents may declare, or only actions. */
export const mcpsOfScope = (scope: 'agent' | 'action'): string[] =>
  tools.mcps.filter((m) => m.scope === scope).map((m) => m.name ?? '');

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
