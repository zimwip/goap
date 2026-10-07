// Model gateway administration types (proto3 JSON).
import type { Int64 } from './common';

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

// ---- the ledger of LLM calls (ADR 0089) ------------------------------------------

/** One LLM call the gateway served or refused, whoever asked. The exchange of an engine call of a change is in the change log; the gateway stores the others (`hasExchange`). */
export interface LLMCall {
  seq: Int64;
  /** RFC 3339 */
  at?: string;
  durationMs?: Int64;
  subject?: string;
  project?: string;
  org?: string;
  /** the name requested, empty for a literal provider/model */
  alias?: string;
  provider?: string;
  model?: string;
  kind?: 'complete' | 'embed' | string;
  inputTokens?: Int64;
  outputTokens?: Int64;
  error?: string;
  /** engine | assistant | helper | indexer | intent | ... */
  source?: string;
  conversationId?: string;
  processId?: string;
  changeId?: string;
  /** -1 (or absent: proto3 omits 0, so test `processId` first) when the call belongs to no process */
  step?: number;
  action?: string;
  agent?: string;
  call?: number;
  /** the gateway stores the request and the answer of this call (`getCallExchange`) */
  hasExchange?: boolean;
  /** the global behaviours the gateway added to the instructions (ADR 0093); a leading `!` marks one dropped by the cap */
  behaviors?: string[];
  /** estimated tokens the behaviours added */
  behaviorTokens?: Int64;
}

/** The exchange the gateway stores for a call (ADR 0089). */
export interface CallExchange {
  call?: LLMCall;
  system?: string;
  messages?: { role?: string; content?: string }[];
  response?: string;
  truncated?: boolean;
  error?: string;
}

/** Every field narrows; empty matches everything. A caller who is not an administrator sees its own calls only. */
export interface UsageFilter {
  /** RFC 3339 */
  from?: string;
  to?: string;
  subject?: string;
  project?: string;
  /** the model id, or provider/model */
  model?: string;
  alias?: string;
  source?: string;
  processId?: string;
  changeId?: string;
  conversationId?: string;
  /** only the calls after this seq (the cursor of a feed) */
  afterSeq?: Int64;
  /** 0: 500; at most 5000 */
  limit?: number;
}

export interface ListUsageResponse {
  /** ascending by seq */
  calls?: LLMCall[];
  nextSeq?: Int64;
  hasMore?: boolean;
}

export type UsageGroup = 'model' | 'alias' | 'source' | 'subject' | 'process' | 'action' | 'agent' | 'day' | 'hour';

export interface UsageSummaryRow {
  /** day: YYYY-MM-DD, hour: YYYY-MM-DDTHH (UTC), empty for calls with none */
  key?: string;
  calls?: Int64;
  inputTokens?: Int64;
  outputTokens?: Int64;
  errors?: Int64;
  durationMs?: Int64;
}

// ---- global behaviours of the LLM calls (ADR 0093) ---------------------------------

/** An instruction the gateway adds to the system text of the calls matching its scope; every selector is optional and ANDed. */
export interface LlmBehavior {
  name: string;
  description?: string;
  instruction?: string;
  enabled?: boolean;
  /** `prepend` or `append` (default) to the system text */
  position?: 'prepend' | 'append' | string;
  /** lower first, then by name */
  order?: number;
  aliases?: string[];
  /** provider/model */
  models?: string[];
  /** engine | assistant | helper | indexer | intent | other */
  sources?: string[];
  /** complete only: a behaviour never applies to an embedding */
  kinds?: string[];
  /** also apply to calls that require a JSON answer */
  appliesToJson?: boolean;
}

export interface ListBehaviorsResponse {
  behaviors?: LlmBehavior[];
  maxInstructionBytes?: number;
  maxTotalBytes?: number;
  sources?: string[];
}

export interface PreviewBehaviorsRequest {
  alias: string;
  source: string;
  json: boolean;
  system: string;
}

export interface PreviewBehaviorsResponse {
  system?: string;
  applied?: string[];
  skipped?: string[];
  addedTokens?: number;
}
