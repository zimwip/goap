//! The Connect `goap.model.v1.ModelService` of the gateway and its client.

use std::collections::HashSet;
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_authz as authz;
use goap_connect::{Code, ConnectError, Request, Response};
use goap_ctx::Ctx;
use goap_gen::model::v1 as pb;
use goap_gen::model::v1::ModelService;
use goap_identity::Extractor;
use goap_llm as llm;
use goap_llmcfg as llmcfg;
use serde_json::{Map, Value};

use crate::behaviorcost::CostView;
use crate::error::Error;
use crate::ledger::{Call, UsageFilter};
use crate::protocols::{kinds, protocols};
use crate::router::parse_target;
use crate::service::{CatalogEntry, ProviderView, Service};
use crate::store::{AliasEntry, ModelEntry, ProviderRecord};
use crate::suggest::{SuggestContext, SuggestField, SuggestInput, SuggestMessage, SuggestModel};

/// The permission required to administer the gateway.
pub const ADMIN_ACTION: &str = "admin";
pub const ADMIN_RESOURCE: &str = "platform";

/// Handler implements `goap_gen::model::v1::ModelService`; mount it with `model_service_router`.
#[derive(Clone)]
pub struct Handler {
    pub service: Arc<Service>,
    pub identity: Extractor,
    /// Guards the administration (ADMIN_ACTION on ADMIN_RESOURCE).
    pub authz: Option<Arc<dyn authz::Authorizer>>,
}

impl Handler {
    /// A handler over a service, with no identity default and no authorizer.
    pub fn new(service: Arc<Service>) -> Self {
        Handler {
            service,
            identity: Extractor::default(),
            authz: None,
        }
    }
}

/// The Connect error of an error of the gateway.
pub fn rpc_err(err: Error) -> ConnectError {
    let code = if err.is_forbidden() {
        Code::PermissionDenied
    } else if err.is_quota_exceeded() {
        Code::ResourceExhausted
    } else if err.is_model_disabled() {
        Code::FailedPrecondition
    } else if err.is_invalid() {
        Code::InvalidArgument
    } else if err.is_not_found() {
        Code::NotFound
    } else {
        match err {
            Error::Rpc(ce) => return ce,
            e => return ConnectError::from_error(Code::Internal, e),
        }
    };
    ConnectError::from_error(code, err)
}

/// The policy errors answered with their code, any other as unavailable (the provider failed).
fn policy_or_unavailable(err: Error) -> ConnectError {
    if err.is_policy() {
        rpc_err(err)
    } else {
        ConnectError::from_error(Code::Unavailable, err)
    }
}

fn admin_request(ctx: &Ctx) -> authz::Request {
    authz::Request {
        subject: authz::principal_from(ctx),
        action: ADMIN_ACTION.into(),
        resource: authz::Resource {
            r#type: ADMIN_RESOURCE.into(),
            ..Default::default()
        },
    }
}

impl Handler {
    async fn guard(&self, ctx: &Ctx, hdr: &http::HeaderMap) -> Result<Ctx, ConnectError> {
        let ctx = self.identity.context(ctx, hdr);
        if let Some(a) = &self.authz {
            match authz::check(&ctx, Some(a.as_ref()), &admin_request(&ctx)).await {
                Ok(()) => {}
                Err(e) if e.is_forbidden() => {
                    return Err(ConnectError::from_error(Code::PermissionDenied, e));
                }
                Err(e) => return Err(ConnectError::from_error(Code::Internal, e)),
            }
        }
        Ok(ctx)
    }

    /// Whether the caller administers the platform and so may read the calls of any subject. Without an authorizer
    /// nobody is: the ledger is then readable by its callers for their own calls only.
    async fn is_admin(&self, ctx: &Ctx) -> bool {
        match &self.authz {
            None => false,
            Some(a) => authz::check(ctx, Some(a.as_ref()), &admin_request(ctx))
                .await
                .is_ok(),
        }
    }
}

fn usage_to_pb(u: &llm::Usage) -> pb::Usage {
    pb::Usage {
        input_tokens: u.input_tokens as i32,
        output_tokens: u.output_tokens as i32,
        cache_read_tokens: u.cache_read_tokens as i32,
        cache_write_tokens: u.cache_write_tokens as i32,
    }
}

fn alias_to_pb(a: &AliasEntry) -> Option<pb::ModelAlias> {
    parse_target(&a.target).map(|t| pb::ModelAlias {
        alias: a.alias.clone(),
        provider: t.provider,
        model: t.model,
        protected: a.protected,
    })
}

fn provider_to_pb(v: &ProviderView) -> pb::Provider {
    pb::Provider {
        name: v.provider.name.clone(),
        kind: v.provider.kind.clone(),
        protocol: v.provider.protocol.clone(),
        base_url: v.provider.base_url.clone(),
        enabled: v.provider.enabled,
        has_key: v.has_key,
        api_key_ref: v.provider.api_key_ref.clone(),
        active: v.active,
    }
}

fn provider_from_pb(p: Option<pb::Provider>) -> ProviderRecord {
    let Some(p) = p else {
        return ProviderRecord::default();
    };
    ProviderRecord {
        name: p.name,
        kind: p.kind,
        protocol: p.protocol,
        base_url: p.base_url,
        enabled: p.enabled,
        api_key_ref: p.api_key_ref,
    }
}

fn catalog_to_pb(e: &CatalogEntry) -> pb::CatalogModel {
    pb::CatalogModel {
        provider: e.model.provider.clone(),
        model: e.model.model.clone(),
        display_name: e.model.display_name.clone(),
        enabled: e.model.enabled,
        quota_tokens: e.model.quota_tokens,
        quota_period: e.model.quota_period.clone(),
        roles: e.model.roles.clone(),
        used_tokens: e.used,
    }
}

fn behavior_to_pb(b: &llmcfg::Behavior) -> pb::Behavior {
    pb::Behavior {
        name: b.name.clone(),
        description: b.description.clone(),
        instruction: b.instruction.clone(),
        enabled: b.enabled,
        position: b.position.clone(),
        order: b.order as i32,
        aliases: b.aliases.clone(),
        models: b.models.clone(),
        sources: b.sources.clone(),
        kinds: b.kinds.clone(),
        applies_to_json: b.applies_to_json,
        costs: Vec::new(),
    }
}

fn costs_to_pb(cs: &[CostView]) -> Vec<pb::BehaviorCost> {
    cs.iter()
        .map(|c| pb::BehaviorCost {
            behavior: c.behavior.clone(),
            model: format!("{}/{}", c.provider, c.model),
            aliases: c.aliases.clone(),
            tokens: c.tokens,
            baseline_tokens: c.baseline,
            source: c.source.clone(),
            measured_at_ms: c.measured_at.map(|t| t.timestamp_millis()).unwrap_or(0),
            error: c.error.clone(),
        })
        .collect()
}

/// The declaration of the calls made with ctx, for the ledger of the gateway (ADR 0089).
pub(crate) fn meta_to_pb(ctx: &Ctx) -> pb::CallMeta {
    let m = llm::meta_from(ctx);
    pb::CallMeta {
        source: m.source,
        conversation_id: m.conversation_id,
        process_id: m.process_id,
        change_id: m.change_id,
        step: m.step as i32,
        action: m.action,
        agent: m.agent,
        call: m.call as i32,
    }
}

/// The declaration of a request, cleaned (the caller is not trusted with it beyond accounting).
pub(crate) fn meta_from_pb(m: Option<pb::CallMeta>) -> llm::CallMeta {
    let Some(m) = m else {
        return llm::CallMeta::default().clean();
    };
    llm::CallMeta {
        source: m.source,
        conversation_id: m.conversation_id,
        process_id: m.process_id,
        change_id: m.change_id,
        step: m.step.into(),
        action: m.action,
        agent: m.agent,
        call: m.call.into(),
    }
    .clean()
}

fn timestamp(t: DateTime<Utc>) -> pbjson_types::Timestamp {
    pbjson_types::Timestamp {
        seconds: t.timestamp(),
        nanos: t.timestamp_subsec_nanos() as i32,
    }
}

fn from_timestamp(t: &pbjson_types::Timestamp) -> DateTime<Utc> {
    DateTime::from_timestamp(t.seconds, t.nanos.max(0) as u32).unwrap_or_default()
}

pub(crate) fn filter_from_pb(f: Option<pb::UsageFilter>) -> UsageFilter {
    let Some(f) = f else {
        return UsageFilter::default();
    };
    UsageFilter {
        from: f.from.as_ref().map(from_timestamp),
        to: f.to.as_ref().map(from_timestamp),
        subject: f.subject,
        end_user: f.end_user,
        project: f.project,
        model: f.model,
        alias: f.alias,
        source: f.source,
        process_id: f.process_id,
        change_id: f.change_id,
        conversation_id: f.conversation_id,
        after_seq: f.after_seq,
        limit: f.limit.into(),
    }
}

pub(crate) fn call_to_pb(c: &Call) -> pb::LlmCall {
    pb::LlmCall {
        seq: c.seq,
        at: Some(timestamp(c.at)),
        duration_ms: c.duration_ms,
        subject: c.subject.clone(),
        end_user: c.end_user.clone(),
        project: c.project.clone(),
        org: c.org.clone(),
        alias: c.alias.clone(),
        provider: c.provider.clone(),
        model: c.model.clone(),
        kind: c.kind.clone(),
        input_tokens: c.input_tokens,
        output_tokens: c.output_tokens,
        error: c.error.clone(),
        source: c.source.clone(),
        conversation_id: c.conversation_id.clone(),
        process_id: c.process_id.clone(),
        change_id: c.change_id.clone(),
        step: c.step as i32,
        action: c.action.clone(),
        agent: c.agent.clone(),
        call: c.call_index as i32,
        has_exchange: c.has_exchange,
        behaviors: c.behaviors.clone(),
        behavior_tokens: c.behavior_tokens,
        behavior_tokens_estimated: c.behavior_tokens_estimated,
    }
}

fn suggest_from_pb(m: pb::SuggestRequest) -> SuggestInput {
    let mut inp = SuggestInput {
        instruction: m.instruction,
        ..Default::default()
    };
    if let Some(c) = m.context {
        let mut sc = SuggestContext {
            subject: c.subject,
            selection: c.selection,
            ..Default::default()
        };
        if let Some(t) = c.tab {
            sc.tab_kind = t.kind;
            sc.tab_params = t.params.into_iter().collect();
        }
        sc.fields = c
            .fields
            .into_iter()
            .map(|f| SuggestField {
                id: f.id,
                label: f.label,
                r#type: f.r#type,
                enum_values: f.enum_values,
                description: f.description,
                current: f.current_value,
                read_only: f.read_only,
            })
            .collect();
        inp.context = sc;
    }
    inp.messages = m
        .messages
        .into_iter()
        .map(|x| SuggestMessage {
            role: x.role,
            text: x.text,
        })
        .collect();
    inp
}

#[async_trait]
impl ModelService for Handler {
    async fn complete(
        &self,
        ctx: &Ctx,
        r: Request<pb::CompleteRequest>,
    ) -> Result<Response<pb::CompleteResponse>, ConnectError> {
        // callers without identity headers (and no dev default) are trusted internal services
        let ctx = self.identity.context(ctx, &r.headers);
        let m = r.msg;
        let ctx = llm::with_meta(&ctx, meta_from_pb(m.meta));
        let req = llm::Request {
            model: m.model,
            system: m.system,
            max_tokens: m.max_tokens.into(),
            json: m.json,
            cache: m.cache,
            messages: messages_from_pb(&m.messages),
            tools: tools_from_pb(&m.tools),
        };
        let resp = self
            .service
            .complete(&ctx, req)
            .await
            .map_err(policy_or_unavailable)?;
        Ok(Response::new(pb::CompleteResponse {
            text: resp.text,
            provider: resp.provider,
            model: resp.model,
            stop_reason: resp.stop_reason,
            tool_calls: calls_to_pb(&resp.tool_calls),
            usage: Some(usage_to_pb(&resp.usage)),
            behaviors: resp.behaviors,
            behavior_tokens: resp.behavior_tokens as i32,
            behaviors_estimated: resp.behaviors_estimated,
        }))
    }

    async fn list_models(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListModelsRequest>,
    ) -> Result<Response<pb::ListModelsResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let (models, aliases) = self.service.available(&ctx).await.map_err(rpc_err)?;
        let mut out = pb::ListModelsResponse::default();
        let mut seen = HashSet::new();
        for m in &models {
            out.models.push(pb::AvailableModel {
                provider: m.provider.clone(),
                model: m.model.clone(),
                display_name: m.display_name.clone(),
            });
            if seen.insert(m.provider.clone()) {
                out.providers.push(m.provider.clone());
            }
        }
        out.aliases = aliases.iter().filter_map(alias_to_pb).collect();
        Ok(Response::new(out))
    }

    async fn embed(
        &self,
        ctx: &Ctx,
        r: Request<pb::EmbedRequest>,
    ) -> Result<Response<pb::EmbedResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let m = r.msg;
        let ctx = llm::with_meta(&ctx, meta_from_pb(m.meta));
        let resp = self
            .service
            .embed(
                &ctx,
                llm::EmbedRequest {
                    model: m.model,
                    texts: m.texts,
                },
            )
            .await
            .map_err(policy_or_unavailable)?;
        Ok(Response::new(pb::EmbedResponse {
            provider: resp.provider,
            model: resp.model,
            tokens: resp.tokens as i32,
            vectors: resp
                .vectors
                .into_iter()
                .map(|values| pb::Vector { values })
                .collect(),
        }))
    }

    /// The contextual helper (ADR 0086): stateless, as the calling principal.
    async fn suggest(
        &self,
        ctx: &Ctx,
        r: Request<pb::SuggestRequest>,
    ) -> Result<Response<pb::SuggestResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let res = self
            .service
            .suggest(&ctx, suggest_from_pb(r.msg))
            .await
            .map_err(policy_or_unavailable)?;
        Ok(Response::new(pb::SuggestResponse {
            message: res.message,
            usage: Some(pb::Usage {
                input_tokens: res.usage.input_tokens as i32,
                output_tokens: res.usage.output_tokens as i32,
                ..Default::default()
            }),
            proposals: res
                .proposals
                .into_iter()
                .map(|p| pb::SuggestProposal {
                    field_id: p.field_id,
                    value: p.value,
                    rationale: p.rationale,
                })
                .collect(),
        }))
    }

    async fn list_provider_kinds(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListProviderKindsRequest>,
    ) -> Result<Response<pb::ListProviderKindsResponse>, ConnectError> {
        self.guard(ctx, &r.headers).await?;
        Ok(Response::new(pb::ListProviderKindsResponse {
            kinds: kinds()
                .into_iter()
                .map(|k| pb::ProviderKind {
                    id: k.id,
                    label: k.label,
                    protocol: k.protocol,
                    default_base_url: k.default_base_url,
                    key_required: k.key_required,
                    description: k.description,
                })
                .collect(),
            protocols: protocols()
                .into_iter()
                .map(|p| pb::ProviderProtocol {
                    id: p.id,
                    label: p.label,
                })
                .collect(),
        }))
    }

    async fn list_providers(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListProvidersRequest>,
    ) -> Result<Response<pb::ListProvidersResponse>, ConnectError> {
        let ctx = self.guard(ctx, &r.headers).await?;
        let views = self.service.providers(&ctx).await.map_err(rpc_err)?;
        Ok(Response::new(pb::ListProvidersResponse {
            providers: views.iter().map(provider_to_pb).collect(),
        }))
    }

    async fn discover_models(
        &self,
        ctx: &Ctx,
        r: Request<pb::DiscoverModelsRequest>,
    ) -> Result<Response<pb::DiscoverModelsResponse>, ConnectError> {
        let ctx = self.guard(ctx, &r.headers).await?;
        let m = r.msg;
        let found = self
            .service
            .discover(&ctx, provider_from_pb(m.provider), &m.api_key)
            .await
            .map_err(|e| {
                if e.is_invalid() {
                    rpc_err(e)
                } else {
                    // the provider refused or is unreachable: not an internal error
                    ConnectError::from_error(Code::Unavailable, e)
                }
            })?;
        Ok(Response::new(pb::DiscoverModelsResponse {
            models: found
                .into_iter()
                .map(|d| pb::DiscoveredModel {
                    id: d.model.id,
                    display_name: d.model.display_name,
                    registered: d.registered,
                })
                .collect(),
        }))
    }

    async fn store_provider_key(
        &self,
        ctx: &Ctx,
        r: Request<pb::StoreProviderKeyRequest>,
    ) -> Result<Response<pb::StoreProviderKeyResponse>, ConnectError> {
        let ctx = self.guard(ctx, &r.headers).await?;
        let api_key_ref = self
            .service
            .store_provider_key(&ctx, &r.msg.provider_name, &r.msg.api_key)
            .await
            .map_err(|e| {
                if e.is_invalid() {
                    rpc_err(e)
                } else {
                    // Vault unreachable or not configured: not an internal error
                    ConnectError::from_error(Code::Unavailable, e)
                }
            })?;
        Ok(Response::new(pb::StoreProviderKeyResponse { api_key_ref }))
    }

    async fn list_catalog(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListCatalogRequest>,
    ) -> Result<Response<pb::ListCatalogResponse>, ConnectError> {
        let ctx = self.guard(ctx, &r.headers).await?;
        let (models, aliases) = self.service.catalog(&ctx).await.map_err(rpc_err)?;
        let mut out = pb::ListCatalogResponse {
            models: models.iter().map(catalog_to_pb).collect(),
            ..Default::default()
        };
        for a in &aliases {
            if let Some(x) = alias_to_pb(a) {
                out.aliases.push(x);
            } else if a.protected {
                // listed for the administrators to retarget it, resolving to nothing
                out.aliases.push(pb::ModelAlias {
                    alias: a.alias.clone(),
                    protected: true,
                    ..Default::default()
                });
            }
        }
        Ok(Response::new(out))
    }

    /// The global behaviours (ADR 0093); platform administrators only, like the catalog.
    async fn list_behaviors(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListBehaviorsRequest>,
    ) -> Result<Response<pb::ListBehaviorsResponse>, ConnectError> {
        let ctx = self.guard(ctx, &r.headers).await?;
        let list = self.service.behavior_views(&ctx).await.map_err(rpc_err)?;
        Ok(Response::new(pb::ListBehaviorsResponse {
            max_instruction_bytes: llmcfg::MAX_INSTRUCTION as i32,
            max_total_bytes: llmcfg::MAX_BEHAVIOR_BYTES as i32,
            sources: llmcfg::sources().into_iter().map(str::to_string).collect(),
            behaviors: list
                .iter()
                .map(|b| {
                    let mut x = behavior_to_pb(&b.behavior);
                    x.costs = costs_to_pb(&b.costs);
                    x
                })
                .collect(),
        }))
    }

    /// The system text a call would be sent with; pure, no model is called. Administrators only.
    async fn preview_behaviors(
        &self,
        ctx: &Ctx,
        r: Request<pb::PreviewBehaviorsRequest>,
    ) -> Result<Response<pb::PreviewBehaviorsResponse>, ConnectError> {
        let ctx = self.guard(ctx, &r.headers).await?;
        let m = r.msg;
        let (ap, price) = self
            .service
            .preview_behaviors(&ctx, &m.alias, &m.source, m.json, &m.system)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::PreviewBehaviorsResponse {
            system: ap.system,
            applied: ap.names,
            skipped: ap.skipped,
            added_tokens: price.tokens as i32,
            added_tokens_estimated: price.estimated,
        }))
    }

    /// Measures the cost of behaviours on the real models now (ADR 0093, "Measured cost"); platform administrators
    /// only. The calls are the gateway's own (source calibration, subject system:modelgw).
    async fn measure_behaviors(
        &self,
        ctx: &Ctx,
        r: Request<pb::MeasureBehaviorsRequest>,
    ) -> Result<Response<pb::MeasureBehaviorsResponse>, ConnectError> {
        let ctx = self.guard(ctx, &r.headers).await?;
        let costs = self
            .service
            .measure_behaviors(&ctx, &r.msg.names, &r.msg.models)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::MeasureBehaviorsResponse {
            costs: costs_to_pb(&costs),
        }))
    }

    /// Reads the ledger: the calls attributed to the caller (its own and those made on its behalf, ADR 0114), any
    /// subject's for an administrator.
    async fn list_usage(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListUsageRequest>,
    ) -> Result<Response<pb::ListUsageResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let admin = self.is_admin(&ctx).await;
        let page = self
            .service
            .list_calls(&ctx, filter_from_pb(r.msg.filter), admin)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::ListUsageResponse {
            calls: page.calls.iter().map(call_to_pb).collect(),
            next_seq: page.next,
            has_more: page.more,
        }))
    }

    /// Groups the ledger, with the same visibility as list_usage.
    async fn usage_summary(
        &self,
        ctx: &Ctx,
        r: Request<pb::UsageSummaryRequest>,
    ) -> Result<Response<pb::UsageSummaryResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let admin = self.is_admin(&ctx).await;
        let m = r.msg;
        let rows = self
            .service
            .summary(&ctx, filter_from_pb(m.filter), &m.group_by, admin)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::UsageSummaryResponse {
            rows: rows
                .into_iter()
                .map(|x| pb::UsageSummaryRow {
                    key: x.key,
                    calls: x.calls,
                    input_tokens: x.input,
                    output_tokens: x.output,
                    errors: x.errors,
                    duration_ms: x.duration_ms,
                })
                .collect(),
        }))
    }

    /// The stored exchange of a call: the caller's own, any for an administrator.
    async fn get_call_exchange(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetCallExchangeRequest>,
    ) -> Result<Response<pb::GetCallExchangeResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let admin = self.is_admin(&ctx).await;
        let (c, x) = self
            .service
            .call_exchange(&ctx, r.msg.seq, admin)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::GetCallExchangeResponse {
            call: Some(call_to_pb(&c)),
            system: x.system,
            response: x.response,
            truncated: x.truncated,
            error: c.error,
            messages: x
                .messages
                .into_iter()
                .map(|m| pb::ExchangeMessage {
                    role: m.role,
                    content: m.content,
                })
                .collect(),
        }))
    }
}

// ---- the client ---------------------------------------------------------------

/// Client adapts the model gateway Connect client to llm::Client, llm::Embedder and SuggestModel.
#[derive(Clone, Debug)]
pub struct Client {
    rpc: pb::ModelServiceClient,
}

impl Client {
    /// A client over a Connect client (its base URL, codec and interceptors, such as `goap_identity::Forward`).
    pub fn new(rpc: goap_connect::Client) -> Self {
        Client {
            rpc: pb::ModelServiceClient::new(rpc),
        }
    }

    /// A client of the gateway at base_url, default settings.
    pub fn connect(base_url: &str) -> Self {
        Client {
            rpc: pb::ModelServiceClient::connect(base_url),
        }
    }

    /// Completes a prompt through the gateway.
    pub async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, Error> {
        let inp = pb::CompleteRequest {
            model: req.model,
            system: req.system,
            max_tokens: req.max_tokens as i32,
            json: req.json,
            cache: req.cache,
            meta: Some(meta_to_pb(ctx)),
            messages: messages_to_pb(&req.messages),
            tools: tools_to_pb(&req.tools),
        };
        let r = self.rpc.complete(ctx, Request::new(inp)).await?.msg;
        let mut out = llm::Response {
            text: r.text,
            provider: r.provider,
            model: r.model,
            stop_reason: r.stop_reason,
            tool_calls: calls_from_pb(&r.tool_calls),
            behaviors: r.behaviors,
            behavior_tokens: r.behavior_tokens.into(),
            behaviors_estimated: r.behaviors_estimated,
            ..Default::default()
        };
        if let Some(u) = r.usage {
            out.usage = llm::Usage {
                input_tokens: u.input_tokens.into(),
                output_tokens: u.output_tokens.into(),
                cache_read_tokens: u.cache_read_tokens.into(),
                cache_write_tokens: u.cache_write_tokens.into(),
            };
        }
        Ok(out)
    }

    /// What the caller may use: the models and the aliases pointing to them (the contextual helper and the
    /// assistant check their alias with it).
    pub async fn available(&self, ctx: &Ctx) -> Result<(Vec<ModelEntry>, Vec<AliasEntry>), Error> {
        let r = self
            .rpc
            .list_models(ctx, Request::new(pb::ListModelsRequest {}))
            .await?
            .msg;
        let models = r
            .models
            .into_iter()
            .map(|m| ModelEntry {
                provider: m.provider,
                model: m.model,
                display_name: m.display_name,
                enabled: true,
                ..Default::default()
            })
            .collect();
        let aliases = r
            .aliases
            .into_iter()
            .map(|a| AliasEntry {
                alias: a.alias,
                target: format!("{}/{}", a.provider, a.model),
                protected: a.protected,
            })
            .collect();
        Ok((models, aliases))
    }

    /// Embeds texts through the gateway.
    pub async fn embed(
        &self,
        ctx: &Ctx,
        req: llm::EmbedRequest,
    ) -> Result<llm::EmbedResponse, Error> {
        let r = self
            .rpc
            .embed(
                ctx,
                Request::new(pb::EmbedRequest {
                    model: req.model,
                    texts: req.texts,
                    meta: Some(meta_to_pb(ctx)),
                }),
            )
            .await?
            .msg;
        Ok(llm::EmbedResponse {
            provider: r.provider,
            model: r.model,
            tokens: r.tokens.into(),
            vectors: r.vectors.into_iter().map(|v| v.values).collect(),
        })
    }
}

#[async_trait]
impl llm::Client for Client {
    async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, llm::Error> {
        Client::complete(self, ctx, req)
            .await
            .map_err(Error::into_llm)
    }
}

#[async_trait]
impl llm::Embedder for Client {
    async fn embed(
        &self,
        ctx: &Ctx,
        req: llm::EmbedRequest,
    ) -> Result<llm::EmbedResponse, llm::Error> {
        Client::embed(self, ctx, req).await.map_err(Error::into_llm)
    }
}

#[async_trait]
impl SuggestModel for Client {
    async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, Error> {
        Client::complete(self, ctx, req).await
    }
    async fn available(&self, ctx: &Ctx) -> Result<(Vec<ModelEntry>, Vec<AliasEntry>), Error> {
        Client::available(self, ctx).await
    }
}

// ---- tools over the wire (ADR 0098) ---------------------------------------

fn json_text(m: &Map<String, Value>) -> String {
    crate::tools::go_json(m)
}

fn json_object(s: &str) -> Map<String, Value> {
    if s.is_empty() {
        return Map::new();
    }
    match serde_json::from_str::<Value>(s) {
        Ok(Value::Object(m)) => m,
        _ => Map::new(),
    }
}

pub(crate) fn calls_to_pb(calls: &[llm::ToolCall]) -> Vec<pb::ToolCall> {
    calls
        .iter()
        .map(|c| pb::ToolCall {
            id: c.id.clone(),
            name: c.name.clone(),
            arguments_json: json_text(&c.arguments),
        })
        .collect()
}

pub(crate) fn calls_from_pb(calls: &[pb::ToolCall]) -> Vec<llm::ToolCall> {
    calls
        .iter()
        .map(|c| llm::ToolCall {
            id: c.id.clone(),
            name: c.name.clone(),
            arguments: json_object(&c.arguments_json),
        })
        .collect()
}

pub(crate) fn messages_to_pb(msgs: &[llm::Message]) -> Vec<pb::Message> {
    msgs.iter()
        .map(|m| pb::Message {
            role: m.role.clone(),
            content: m.content.clone(),
            tool_calls: calls_to_pb(&m.tool_calls),
            tool_results: m
                .tool_results
                .iter()
                .map(|r| pb::ToolResult {
                    call_id: r.call_id.clone(),
                    name: r.name.clone(),
                    content: r.content.clone(),
                    is_error: r.is_error,
                })
                .collect(),
        })
        .collect()
}

pub(crate) fn messages_from_pb(msgs: &[pb::Message]) -> Vec<llm::Message> {
    msgs.iter()
        .map(|m| llm::Message {
            role: m.role.clone(),
            content: m.content.clone(),
            tool_calls: calls_from_pb(&m.tool_calls),
            tool_results: m
                .tool_results
                .iter()
                .map(|r| llm::ToolResult {
                    call_id: r.call_id.clone(),
                    name: r.name.clone(),
                    content: r.content.clone(),
                    is_error: r.is_error,
                })
                .collect(),
        })
        .collect()
}

pub(crate) fn tools_to_pb(tools: &[llm::ToolSpec]) -> Vec<pb::ToolSpec> {
    tools
        .iter()
        .map(|t| pb::ToolSpec {
            name: t.name.clone(),
            description: t.description.clone(),
            input_schema_json: json_text(&t.input_schema),
        })
        .collect()
}

pub(crate) fn tools_from_pb(tools: &[pb::ToolSpec]) -> Vec<llm::ToolSpec> {
    tools
        .iter()
        .map(|t| llm::ToolSpec {
            name: t.name.clone(),
            description: t.description.clone(),
            input_schema: json_object(&t.input_schema_json),
        })
        .collect()
}
