import { rpc } from './transport';
import type { Empty, Int64, Struct } from './types/common';
import type { BehaviorCost, ListBehaviorsResponse, MeasureBehaviorsRequest, PreviewBehaviorsRequest, PreviewBehaviorsResponse, AvailableModel, CallExchange, CatalogModel, DiscoveredModel, ListUsageResponse, LlmProvider, ModelAlias, ProviderKind, SuggestRequest, SuggestResponse, UsageFilter, UsageGroup, UsageSummaryRow } from './types/models';

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
  /** Writes a raw key to Vault and returns the apiKeyRef to store; fails if Vault is not configured server-side. */
  storeProviderKey: (providerName: string, apiKey: string) =>
    rpc<{ providerName: string; apiKey: string }, { apiKeyRef?: string }>(MODEL, 'StoreProviderKey', { providerName, apiKey }),
  listCatalog: (signal?: AbortSignal) =>
    rpc<Empty, { models?: CatalogModel[]; aliases?: ModelAlias[] }>(MODEL, 'ListCatalog', {}, signal),
  /** The global behaviours of the LLM calls (ADR 0093), enabled and disabled; administrators only. Edited with `llmEdit`. */
  listBehaviors: (signal?: AbortSignal) => rpc<Empty, ListBehaviorsResponse>(MODEL, 'ListBehaviors', {}, signal),
  /** The system text a call would be sent with, without calling any model (pure). */
  previewBehaviors: (req: PreviewBehaviorsRequest, signal?: AbortSignal) => rpc<PreviewBehaviorsRequest, PreviewBehaviorsResponse>(MODEL, 'PreviewBehaviors', req, signal),
  /** Measure now, on the real models, what behaviours cost in input tokens (ADR 0093, "Measured cost"); administrators only. */
  measureBehaviors: (req: MeasureBehaviorsRequest, signal?: AbortSignal) => rpc<MeasureBehaviorsRequest, { costs?: BehaviorCost[] }>(MODEL, 'MeasureBehaviors', req, signal),
  /** The contextual helper (ADR 0086): proposes values for the fields of a form. Stateless, nothing is stored. */
  suggest: (req: SuggestRequest, signal?: AbortSignal) => rpc<SuggestRequest, SuggestResponse>(MODEL, 'Suggest', req, signal),
  /** The ledger of LLM calls (ADR 0089): a caller reads its own calls, an administrator any subject or the whole platform. */
  listUsage: (filter: UsageFilter, signal?: AbortSignal) =>
    rpc<{ filter: UsageFilter }, ListUsageResponse>(MODEL, 'ListUsage', { filter }, signal),
  /** The stored exchange of a call of the ledger (`LLMCall.hasExchange`): own calls, any for an administrator; not_found when purged or not stored. */
  getCallExchange: (seq: Int64, signal?: AbortSignal) => rpc<{ seq: Int64 }, CallExchange>(MODEL, 'GetCallExchange', { seq }, signal),
  usageSummary: (filter: UsageFilter, groupBy: UsageGroup, signal?: AbortSignal) =>
    rpc<{ filter: UsageFilter; groupBy: UsageGroup }, { rows?: UsageSummaryRow[] }>(MODEL, 'UsageSummary', { filter, groupBy }, signal),
};
