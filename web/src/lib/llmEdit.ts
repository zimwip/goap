// The model gateway configuration is graph data (platform namespace): providers, catalog models and aliases are
// nodes. The settings dialog does not apply its edits: it stages them as impacts of a personal change
// (stores/pending.svelte.ts) that the user saves or discards. An API key is never stored, only its reference.
import { graph, type CatalogModel, type LlmProvider, type ModelAlias, type Struct } from './api';
import { headGraph } from './graphEdit';
import { checkinAccepted } from './lifecycle';
import { ns, isUserKey } from './stores/session.svelte';
import { pending, stageRetire, stageUpsert, stagedOfType } from './stores/pending.svelte';

export const PROVIDER_TYPE = 'platform@LlmProvider';
export const MODEL_TYPE = 'platform@LlmModel';
export const ALIAS_TYPE = 'platform@LlmAlias';

export const providerKey = (name: string) => `LLP:${name}`;
export const modelKey = (provider: string, model: string) => `LLM:${provider}/${model}`;
export const aliasKey = (alias: string) => `LLA:${alias}`;

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

const str = (v: unknown) => (typeof v === 'string' ? v : '');

// --- staging ------------------------------------------------------------------------------------------------------

/** Stages the creation or the update of a provider. */
export const saveProvider = (p: LlmProvider) => stageUpsert(ns.platform, PROVIDER_TYPE, providerKey(p.name), providerProps(p));

/** Adds or updates models of the catalog. */
export async function saveModels(ms: CatalogModel[]): Promise<void> {
  for (const m of ms) await stageUpsert(ns.platform, MODEL_TYPE, modelKey(m.provider, m.model), modelProps(m));
}

export const saveModel = (m: CatalogModel) => saveModels([m]);

export const saveAlias = (a: ModelAlias) =>
  stageUpsert(ns.platform, ALIAS_TYPE, aliasKey(a.alias), { alias: a.alias, target: `${a.provider}/${a.model}` });

/** The nodes of the platform namespace as the user sees them: main with the pending edits laid over it. */
async function effective(): Promise<{ key: string; type: string; props: Record<string, unknown> }[]> {
  const h = await headGraph(ns.platform);
  const out = new Map<string, { key: string; type: string; props: Record<string, unknown> }>();
  for (const n of h.nodes) if (n.namespace === ns.platform && !n.deleted && n.state !== 'retired' && n.key && n.type) out.set(n.key, { key: n.key, type: n.type, props: (n.props ?? {}) as Record<string, unknown> });
  for (const s of Object.values(pending.byNs[ns.platform]?.nodes ?? {})) {
    if (s.retire) out.delete(s.key);
    else out.set(s.key, { key: s.key, type: s.type, props: { ...(out.get(s.key)?.props ?? {}), ...s.props } });
  }
  return [...out.values()];
}

/** Stages the removal of a model with the aliases that point to it. */
export async function deleteModel(provider: string, model: string): Promise<void> {
  const target = `${provider}/${model}`;
  const nodes = await effective();
  for (const a of nodes.filter((n) => n.type === ALIAS_TYPE && str(n.props.target) === target)) await stageRetire(ns.platform, ALIAS_TYPE, a.key);
  if (nodes.some((n) => n.key === modelKey(provider, model))) await stageRetire(ns.platform, MODEL_TYPE, modelKey(provider, model));
}

/** Stages the removal of a provider with its models and the aliases that point to them. */
export async function deleteProvider(name: string): Promise<void> {
  const nodes = await effective();
  for (const a of nodes.filter((n) => n.type === ALIAS_TYPE && str(n.props.target).startsWith(`${name}/`))) await stageRetire(ns.platform, ALIAS_TYPE, a.key);
  for (const m of nodes.filter((n) => n.type === MODEL_TYPE && str(n.props.provider) === name)) await stageRetire(ns.platform, MODEL_TYPE, m.key);
  if (nodes.some((n) => n.key === providerKey(name))) await stageRetire(ns.platform, PROVIDER_TYPE, providerKey(name));
}

export async function deleteAlias(alias: string): Promise<void> {
  if ((await effective()).some((n) => n.key === aliasKey(alias))) await stageRetire(ns.platform, ALIAS_TYPE, aliasKey(alias));
}

// --- overlay: what the dialog shows -----------------------------------------------------------------------------

/** UI marker of a row that holds an unsaved edit. */
export type Unsaved<T> = T & { pending?: boolean };

function overlay<T extends object>(applied: T[], type: string, id: (t: T) => string, fromProps: (p: Record<string, unknown>, old?: T) => T | undefined): Unsaved<T>[] {
  const rows = new Map<string, Unsaved<T>>(applied.map((t) => [id(t), t]));
  const gone = new Set<string>();
  for (const s of stagedOfType(ns.platform, type)) {
    if (s.retire) {
      // the key of a node is derived from its properties: find the row it stands for
      for (const [k, t] of rows) if (rowKey(type, t) === s.key) gone.add(k);
      continue;
    }
    const p = s.props as Record<string, unknown>;
    const key = [...rows.entries()].find(([, t]) => rowKey(type, t) === s.key)?.[0];
    const merged = fromProps(p, key ? rows.get(key) : undefined);
    if (merged) rows.set(key ?? id(merged), { ...merged, pending: true });
  }
  for (const k of gone) rows.delete(k);
  return [...rows.values()];
}

const rowKey = (type: string, t: object): string => {
  if (type === PROVIDER_TYPE) return providerKey((t as LlmProvider).name);
  if (type === MODEL_TYPE) return modelKey((t as CatalogModel).provider, (t as CatalogModel).model);
  return aliasKey((t as ModelAlias).alias);
};

export const overlayProviders = (applied: LlmProvider[]): Unsaved<LlmProvider>[] =>
  overlay(applied, PROVIDER_TYPE, (p) => p.name, (p, old) => {
    const name = str(p.name) || old?.name;
    if (!name) return undefined;
    return { ...old, name, kind: str(p.kind) || old?.kind || '', protocol: str(p.protocol) || old?.protocol || '', baseUrl: str(p.baseURL) || (p.baseURL === undefined ? old?.baseUrl : ''), enabled: typeof p.enabled === 'boolean' ? p.enabled : (old?.enabled ?? true), apiKeyRef: str(p.apiKeyRef) || (p.apiKeyRef === undefined ? old?.apiKeyRef : ''), hasKey: !!(str(p.apiKeyRef) || old?.hasKey), active: old?.active ?? false };
  });

export const overlayCatalog = (applied: CatalogModel[]): Unsaved<CatalogModel>[] =>
  overlay(applied, MODEL_TYPE, (m) => `${m.provider}/${m.model}`, (p, old) => {
    const provider = str(p.provider) || old?.provider;
    const model = str(p.model) || old?.model;
    if (!provider || !model) return undefined;
    return { ...old, provider, model, displayName: str(p.displayName) || old?.displayName || model, enabled: typeof p.enabled === 'boolean' ? p.enabled : old?.enabled, quotaTokens: typeof p.quotaTokens === 'number' ? p.quotaTokens : (p.quotaTokens === undefined && old ? old.quotaTokens : 0), quotaPeriod: str(p.quotaPeriod) || old?.quotaPeriod || 'month', roles: Array.isArray(p.roles) ? (p.roles as string[]) : (old?.roles ?? []), usedTokens: old?.usedTokens ?? 0 };
  });

export const overlayAliases = (applied: ModelAlias[]): Unsaved<ModelAlias>[] =>
  overlay(applied, ALIAS_TYPE, (a) => a.alias, (p, old) => {
    const alias = str(p.alias) || old?.alias;
    const target = str(p.target);
    if (!alias || !target.includes('/')) return old;
    const [provider, ...rest] = target.split('/');
    return { alias, provider, model: rest.join('/') };
  });

// --- alias proposals ----------------------------------------------------------------------------------------------

/** An alias a methodology references and nobody configured yet: proposed by an open change, waiting for a review. */
export interface AliasProposal {
  changeId: string;
  impactId: string;
  alias: string;
  /** provider/model, or '' while nobody chose one */
  target: string;
  reason: string;
  by: string;
}

/** The alias impacts other people (or the platform) proposed and left open: shown with Accept / Decline. */
export async function listAliasProposals(): Promise<AliasProposal[]> {
  const out: AliasProposal[] = [];
  const open = (await graph.listChanges()).changes?.filter((c) => c.namespace === ns.platform && (c.status === 'draft' || c.status === 'active') && !isUserKey(c.ownerOrg)) ?? [];
  for (const ch of open) {
    if (!ch.id) continue;
    const board = (await graph.getBlackboard(ch.id, '')).change;
    for (const imp of board?.nodes ?? []) {
      if (imp.type !== ALIAS_TYPE || imp.review !== 'proposed' || imp.superseded || !imp.id) continue;
      const n = imp.post ? (await graph.getNode(imp.post)).view?.node : undefined;
      const props = (n?.props ?? {}) as Record<string, unknown>;
      out.push({ changeId: ch.id, impactId: imp.id, alias: str(props.alias) || (imp.key ?? '').replace(/^LLA:/, ''), target: str(props.target), reason: imp.rationale ?? ch.intent ?? '', by: imp.producedBy ?? '' });
    }
  }
  return out;
}

/** Accepts a proposed alias pointing to `target` and applies the change that proposed it. */
export async function acceptAliasProposal(p: AliasProposal, target: string): Promise<void> {
  // the target is set on the working version of the proposal (checked out again when it was checked in, ADR 0076)
  const imp = (await graph.getBlackboard(p.changeId, '')).change?.nodes?.find((n) => n.id === p.impactId);
  const work = imp?.post ? (await graph.getNode(imp.post)).view?.node : undefined;
  if (!work?.checkedOut) await graph.impactNodeCheckout(p.changeId, { changeImpactId: p.impactId }, `Accept the alias ${p.alias}`);
  await graph.impactNodeUpdate(p.changeId, p.impactId, { props: { alias: p.alias, target } });
  await graph.impactNodeReview(p.changeId, p.impactId, true, `Accepted with target ${target}`);
  await checkinAccepted(p.changeId);
  await graph.applyChange(p.changeId, '');
}

/** Declines a proposed alias: the change that proposed it is abandoned. */
export async function declineAliasProposal(p: AliasProposal): Promise<void> {
  await graph.impactNodeReview(p.changeId, p.impactId, false, 'Declined');
  await graph.updateChange(p.changeId, { status: 'abandoned' });
}
