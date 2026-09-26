// State of the "Tools" explorer: connectors, MCPs, adapters, organisations and their bindings.
import { mcp, iam, errorMessage, type Adapter, type Binding, type Connector, type Mcp, type Organization } from '../api';

export const tools = $state({
  connectors: [] as Connector[],
  mcps: [] as Mcp[],
  adapters: [] as Adapter[],
  orgs: [] as Organization[],
  bindings: [] as Binding[],
  loading: false,
  loaded: false,
  error: '',
});

export async function refreshTools(): Promise<void> {
  tools.loading = true;
  try {
    const [c, m, a, o] = await Promise.all([mcp.listConnectors(), mcp.listMcps(), mcp.listAdapters(), iam.listOrganizations()]);
    const orgs = o.organizations ?? [];
    const per = await Promise.all(orgs.map((org) => mcp.listBindings(org.id ?? '')));
    tools.connectors = c.connectors ?? [];
    tools.mcps = m.mcps ?? [];
    tools.adapters = a.adapters ?? [];
    tools.orgs = orgs;
    tools.bindings = per.flatMap((r) => r.bindings ?? []);
    tools.error = '';
  } catch (e) {
    tools.error = errorMessage(e);
  } finally {
    tools.loading = false;
    tools.loaded = true;
  }
}
