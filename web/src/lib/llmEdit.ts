// The model gateway configuration is graph data (platform namespace): providers, catalog models and aliases are
// nodes, changed through one change applied on main. An API key is never stored, only its reference.
import type { CatalogModel, GraphNode, LlmProvider, ModelAlias, Struct } from './api';
import { applyOnMain, createNodeItem, deleteNodeItem, headGraph, updateNodeItem, type HeadGraph } from './graphEdit';
import type { NodeEdit } from './api';

export const NS_PLATFORM = 'platform';
export const PROVIDER_TYPE = 'platform@LlmProvider';
export const MODEL_TYPE = 'platform@LlmModel';
export const ALIAS_TYPE = 'platform@LlmAlias';

export const providerKey = (name: string) => `LLP:${name}`;
export const modelKey = (provider: string, model: string) => `LLM:${provider}/${model}`;
export const aliasKey = (alias: string) => `LLA:${alias}`;

const find = (h: HeadGraph, type: string, key: string): GraphNode | undefined =>
  h.nodes.find((n) => n.namespace === NS_PLATFORM && n.type === type && n.key === key);

const providerProps = (p: LlmProvider): Struct => ({
  name: p.name,
  kind: p.kind,
  protocol: p.protocol,
  ...(p.baseUrl ? { baseURL: p.baseUrl } : {}),
  enabled: p.enabled ?? true,
  ...(p.apiKeyRef ? { apiKeyRef: p.apiKeyRef } : {}),
});

const modelProps = (m: CatalogModel): Struct => ({
  provider: m.provider,
  model: m.model,
  displayName: m.displayName || m.model,
  enabled: m.enabled ?? true,
  ...(m.quotaTokens ? { quotaTokens: Number(m.quotaTokens) } : {}),
  quotaPeriod: m.quotaPeriod || 'month',
  ...(m.roles?.length ? { roles: m.roles } : {}),
});

const upsert = (h: HeadGraph, type: string, key: string, props: Struct): NodeEdit => {
  const n = find(h, type, key);
  return n ? updateNodeItem(n, props) : createNodeItem(key, type, props);
};

/** Creates or updates a provider. */
export async function saveProvider(p: LlmProvider): Promise<void> {
  const h = await headGraph();
  await applyOnMain(NS_PLATFORM, `Provider ${p.name}`, 'Configure an LLM provider', h.baselineId, [upsert(h, PROVIDER_TYPE, providerKey(p.name), providerProps(p))]);
}

const prop = (n: GraphNode, k: string) => String((n.props as Record<string, unknown> | undefined)?.[k] ?? '');

/** Deletes a provider with its models and the aliases that point to them. */
export async function deleteProvider(name: string): Promise<void> {
  const h = await headGraph();
  const items: NodeEdit[] = [];
  const models = h.nodes.filter((n) => n.namespace === NS_PLATFORM && n.type === MODEL_TYPE && prop(n, 'provider') === name);
  for (const a of h.nodes.filter((n) => n.namespace === NS_PLATFORM && n.type === ALIAS_TYPE && prop(n, 'target').startsWith(`${name}/`))) items.push(deleteNodeItem(a));
  for (const m of models) items.push(deleteNodeItem(m));
  const p = find(h, PROVIDER_TYPE, providerKey(name));
  if (p) items.push(deleteNodeItem(p));
  if (items.length) await applyOnMain(NS_PLATFORM, `Delete provider ${name}`, 'Remove an LLM provider', h.baselineId, items);
}

/** Adds or updates models of the catalog in one change. */
export async function saveModels(ms: CatalogModel[]): Promise<void> {
  const h = await headGraph();
  const items = ms.map((m, i) => upsert(h, MODEL_TYPE, modelKey(m.provider, m.model), modelProps(m)));
  await applyOnMain(NS_PLATFORM, `Models ${ms.map((m) => `${m.provider}/${m.model}`).join(', ')}`.slice(0, 200), 'Configure models of the catalog', h.baselineId, items);
}

export const saveModel = (m: CatalogModel) => saveModels([m]);

/** Removes a model from the catalog with the aliases that point to it. */
export async function deleteModel(provider: string, model: string): Promise<void> {
  const h = await headGraph();
  const items: NodeEdit[] = h.nodes
    .filter((n) => n.namespace === NS_PLATFORM && n.type === ALIAS_TYPE && prop(n, 'target') === `${provider}/${model}`)
    .map(deleteNodeItem);
  const m = find(h, MODEL_TYPE, modelKey(provider, model));
  if (m) items.push(deleteNodeItem(m));
  if (items.length) await applyOnMain(NS_PLATFORM, `Delete model ${provider}/${model}`, 'Remove a model from the catalog', h.baselineId, items);
}

export async function saveAlias(a: ModelAlias): Promise<void> {
  const h = await headGraph();
  await applyOnMain(NS_PLATFORM, `Alias ${a.alias}`, 'Point an alias to a model', h.baselineId, [
    upsert(h, ALIAS_TYPE, aliasKey(a.alias), { alias: a.alias, target: `${a.provider}/${a.model}` }),
  ]);
}

export async function deleteAlias(alias: string): Promise<void> {
  const h = await headGraph();
  const n = find(h, ALIAS_TYPE, aliasKey(alias));
  if (n) await applyOnMain(NS_PLATFORM, `Delete alias ${alias}`, 'Remove an alias', h.baselineId, [deleteNodeItem(n)]);
}
