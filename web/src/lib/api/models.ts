import { rpc } from './transport';
import type { Empty, Struct } from './types/common';
import type { AvailableModel, CatalogModel, DiscoveredModel, ListUsageResponse, LlmProvider, ModelAlias, ProviderKind, SuggestRequest, SuggestResponse, UsageFilter, UsageGroup, UsageSummaryRow } from './types/models';

const MODEL = 'goap.model.v1.ModelService';
const PREFERENCES = 'goap.preferences.v1.PreferencesService';

/** The personal preferences of the caller, kept outside the graph (ADR 0038): theme, voice input, dashboard defaults. */
export const preferencesApi = {
  get: (signal?: AbortSignal) => rpc<Empty, { values?: Struct }>(PREFERENCES, 'GetPreferences', {}, signal),
  /** Merges values in: a null value clears a key. Answers the preferences after the merge. */
  set: (values: Struct) => rpc<{ values: Struct }, { values?: Struct }>(PREFERENCES, 'SetPreferences', { values }),
  reset: () => rpc<Empty, Empty>(PREFERENCES, 'ResetPreferences', {}),
};

export const models = {
  /** Models (and aliases) the caller may use. */
  listAvailable: (signal?: AbortSignal) =>
    rpc<Empty, { models?: AvailableModel[]; aliases?: ModelAlias[] }>(MODEL, 'ListModels', {}, signal),
  listProviderKinds: (signal?: AbortSignal) =>
    rpc<Empty, { kinds?: ProviderKind[]; protocols?: { id: string; label?: string }[] }>(MODEL, 'ListProviderKinds', {}, signal),
  listProviders: (signal?: AbortSignal) => rpc<Empty, { providers?: LlmProvider[] }>(MODEL, 'ListProviders', {}, signal),
  /** Ask the provider for its models; `apiKey` empty resolves the key from the provider's reference. Read-only: the configuration is edited with `llmEdit`. */
  discoverModels: (provider: Partial<LlmProvider>, apiKey = '') =>
    rpc<object, { models?: DiscoveredModel[] }>(MODEL, 'DiscoverModels', { provider, apiKey }),
  listCatalog: (signal?: AbortSignal) =>
    rpc<Empty, { models?: CatalogModel[]; aliases?: ModelAlias[] }>(MODEL, 'ListCatalog', {}, signal),
  /** The contextual helper (ADR 0086): proposes values for the fields of a form. Stateless, nothing is stored. */
  suggest: (req: SuggestRequest, signal?: AbortSignal) => rpc<SuggestRequest, SuggestResponse>(MODEL, 'Suggest', req, signal),
  /** The ledger of LLM calls (ADR 0089): a caller reads its own calls, an administrator any subject or the whole platform. */
  listUsage: (filter: UsageFilter, signal?: AbortSignal) =>
    rpc<{ filter: UsageFilter }, ListUsageResponse>(MODEL, 'ListUsage', { filter }, signal),
  usageSummary: (filter: UsageFilter, groupBy: UsageGroup, signal?: AbortSignal) =>
    rpc<{ filter: UsageFilter; groupBy: UsageGroup }, { rows?: UsageSummaryRow[] }>(MODEL, 'UsageSummary', { filter, groupBy }, signal),
};
