//! The model gateway. One API over several LLM providers
//! (Anthropic, OpenAI-compatible, Gemini, a fake one), addressed by alias or "provider/model", with the catalog policy
//! (availability, roles, quotas), the global behaviours (ADR 0093) and their measured cost, native tool calling and
//! its text protocol (ADR 0098), the ledger of every call (ADR 0089, 0114), the contextual helper (ADR 0086), the
//! Connect `goap.model.v1.ModelService` and the Anthropic Messages proxy (ADR 0123 §6).

mod anthropicproxy;
mod behaviorcost;
mod config;
mod embed;
mod error;
mod handler;
mod ledger;
mod protocols;
mod providers;
mod retry;
mod router;
mod service;
mod sqlstore;
mod store;
mod suggest;
mod tools;

#[cfg(test)]
mod tests;

pub use anthropicproxy::{ANTHROPIC_PATH, AnthropicProxy};
pub use behaviorcost::{
    BehaviorCost, BehaviorView, COST_ESTIMATED, COST_MEASURED, CostKey, CostView,
    DEFAULT_COST_BACKOFF, DEFAULT_COST_TTL, ModelBaseline, Price,
};
pub use config::{ModelsConfig, boot_config, initial_config};
pub use embed::{Embedder, FAKE_EMBED_DIM};
pub use error::{BoxError, Error};
pub use handler::{ADMIN_ACTION, ADMIN_RESOURCE, Client, Handler, rpc_err};
pub use ledger::{
    Call, CallPage, DEFAULT_CALL_RETENTION_DAYS, DEFAULT_USAGE_LIMIT, Exchange, GROUP_ACTION,
    GROUP_AGENT, GROUP_ALIAS, GROUP_DAY, GROUP_END_USER, GROUP_HOUR, GROUP_MODEL, GROUP_PROCESS,
    GROUP_SOURCE, GROUP_SUBJECT, KIND_COMPLETE, KIND_EMBED, MAX_CALL_ERROR, MAX_USAGE_LIMIT,
    MIN_AUDIT_RETENTION_DAYS, SummaryRow, UsageFilter,
};
pub use protocols::{
    Gemini, Kind, ListModels, ModelInfo, NewProvider, Protocol, ProviderSpec, kinds, lookup_kind,
    lookup_protocol, protocols, register_kind, register_protocol,
};
pub use providers::{ANTHROPIC_BASE, ANTHROPIC_BETA_FALLBACK, Anthropic, Fake, OpenAICompatible};
pub use retry::{DEFAULT_RETRIES, SdkError, StatusError, status_error};
pub use router::{
    CallFn, Config, EmbedCallFn, Instrument, InstrumentEmbed, Objects, Provider, ProviderConfig,
    Router, SecretFunc, Target, build, default_config, load_config,
};
pub use service::{CatalogEntry, Discovered, NowFn, ProviderView, SecretsFn, Service, VaultFn};
pub use sqlstore::{MIGRATIONS, SQLITE_MIGRATIONS, SqlStore};
pub use store::{
    AliasEntry, MemoryStore, ModelEntry, PERIOD_DAY, PERIOD_MONTH, PERIOD_TOTAL, ProviderRecord,
    Store, period_key,
};
pub use suggest::{
    HELPER_ALIAS, MAX_PROPOSAL_BYTES, MAX_SUGGEST_CONTEXT_BYTES, MAX_SUGGEST_FIELDS,
    MAX_SUGGEST_MESSAGE_BYTES, MAX_SUGGEST_MESSAGES, Proposal, SuggestContext, SuggestField,
    SuggestInput, SuggestMessage, SuggestModel, SuggestResult, suggest,
};
