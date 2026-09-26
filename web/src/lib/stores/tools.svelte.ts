// State of the "Connectors" and "MCPs" explorers and of the adapter library: the registry of connectors (hub), the MCPs
// (graph nodes, read through the hub) and the adapter algorithms of the published domains (registry).
import { mcp, registry, errorMessage, type AlgorithmParam, type Connector, type Mcp } from '../api';

/** An adapter of the domain library: an algorithm of type `adapter` of a published domain. */
export interface LibraryAdapter {
  domain: string;
  /** the published version the algorithm was read from (the instance may pin it, or follow the latest) */
  version: string;
  name: string;
  description: string;
  mcp: string;
  connector: string;
  params: AlgorithmParam[];
}

export const tools = $state({
  connectors: [] as Connector[],
  mcps: [] as Mcp[],
  library: [] as LibraryAdapter[],
  loading: false,
  loaded: false,
  error: '',
});

async function loadLibrary(): Promise<LibraryAdapter[]> {
  const out: LibraryAdapter[] = [];
  const list = await registry.listDomains();
  await Promise.all(
    (list.domains ?? []).map(async (s) => {
      if (!s.name) return;
      // latest published version of the domain
      const d = (await registry.getDomain(s.name)).domain;
      for (const a of d?.algorithms ?? []) {
        if (a.type !== 'adapter') continue;
        out.push({
          domain: d?.name ?? s.name,
          version: d?.version ?? '',
          name: a.name ?? '',
          description: a.description ?? '',
          mcp: a.mcp ?? '',
          connector: a.connector ?? '',
          params: a.params ?? [],
        });
      }
    }),
  );
  return out.sort((x, y) => `${x.domain}/${x.name}`.localeCompare(`${y.domain}/${y.name}`));
}

export async function refreshTools(): Promise<void> {
  tools.loading = true;
  try {
    const [c, m, lib] = await Promise.all([mcp.listConnectors(), mcp.listMcps(), loadLibrary()]);
    tools.connectors = c.connectors ?? [];
    tools.mcps = m.mcps ?? [];
    tools.library = lib;
    tools.error = '';
  } catch (e) {
    tools.error = errorMessage(e);
  } finally {
    tools.loading = false;
    tools.loaded = true;
  }
}
