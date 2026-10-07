// Model gateway administration types (proto3 JSON).

export interface ProviderKind {
  id: string;
  label?: string;
  protocol?: string;
  defaultBaseUrl?: string;
  keyRequired?: boolean;
  description?: string;
}

export interface LlmProvider {
  name: string;
  kind: string;
  protocol: string;
  baseUrl?: string;
  enabled?: boolean;
  /** references an API key */
  hasKey?: boolean;
  /** where the key is: `env:<VAR>` or `<vault path>#<field>`, alternatives separated by `|`; the key itself is never stored */
  apiKeyRef?: string;
  /** loaded in the running router */
  active?: boolean;
}

export interface DiscoveredModel {
  id: string;
  displayName?: string;
  registered?: boolean;
}

/** int64 fields travel as strings in proto3 JSON. */
export interface CatalogModel {
  provider: string;
  model: string;
  displayName?: string;
  enabled?: boolean;
  quotaTokens?: string | number;
  quotaPeriod?: string;
  roles?: string[];
  usedTokens?: string | number;
}

export interface ModelAlias {
  alias: string;
  provider: string;
  model: string;
  /** Used by the platform itself (assistant, helper): it cannot be retired nor renamed, only retargeted. */
  protected?: boolean;
}

export interface AvailableModel {
  provider: string;
  model: string;
  displayName?: string;
}

// The contextual helper (ADR 0086).

export interface SuggestField {
  id: string;
  label?: string;
  /** string | number | boolean | date | enum | json */
  type?: string;
  enumValues?: string[];
  description?: string;
  /** the current value as JSON text */
  currentValue?: string;
  readOnly?: boolean;
}

export interface SuggestContext {
  tab: { kind: string; params: Record<string, string> };
  /** the node or change the tab is about (opaque) */
  subject?: string;
  selection?: string;
  fields: SuggestField[];
}

export interface SuggestMessage {
  role: 'user' | 'assistant';
  text: string;
}

export interface SuggestRequest {
  context: SuggestContext;
  messages: SuggestMessage[];
  instruction?: string;
}

export interface SuggestProposal {
  fieldId: string;
  /** the proposed value, as JSON text */
  value: string;
  rationale?: string;
}

export interface SuggestResponse {
  message?: string;
  proposals?: SuggestProposal[];
  usage?: { inputTokens?: number; outputTokens?: number };
}
