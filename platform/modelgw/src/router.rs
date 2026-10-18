//! The model gateway routes completion requests to LLM providers by alias ("default", "fast") or "provider/model".

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use futures::future::BoxFuture;
use goap_ctx::Ctx;
use goap_llm as llm;
use goap_llmcfg as llmcfg;
use parking_lot::RwLock;
use serde::{Deserialize, Serialize};

use crate::embed::Embedder;
use crate::error::{BoxError, Error};
use crate::protocols::lookup_kind;
use crate::providers::{Anthropic, Fake, OpenAICompatible};
use crate::retry::RetryPolicy;
use crate::tools::complete_tools;

/// Provider completes prompts on a given model.
#[async_trait]
pub trait Provider: Send + Sync {
    async fn complete(
        &self,
        ctx: &Ctx,
        model: &str,
        req: llm::Request,
    ) -> Result<llm::Response, llm::Error>;

    /// NativeTools: the provider calls tools through its own API (Go's `NativeTools` interface, tools.go).
    fn native_tools(&self) -> bool {
        false
    }

    /// The provider as an Embedder, when it can embed texts (Go's type assertion on `Embedder`).
    fn embedder(&self) -> Option<&dyn Embedder> {
        None
    }
}

/// Target is a provider model.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash)]
pub struct Target {
    pub provider: String,
    pub model: String,
}

pub(crate) fn parse_target(s: &str) -> Option<Target> {
    let (p, m) = s.split_once('/')?;
    if p.is_empty() || m.is_empty() {
        return None;
    }
    Some(Target {
        provider: p.to_string(),
        model: m.to_string(),
    })
}

/// The future of a provider call, as an instrumentation wraps it.
pub type CallFn =
    Box<dyn FnOnce(Ctx) -> BoxFuture<'static, Result<llm::Response, llm::Error>> + Send + 'static>;

/// Instrument wraps every provider call (OpenTelemetry GenAI spans and metrics): context, provider, model, the
/// request and the call to make.
pub type Instrument = Arc<
    dyn Fn(
            Ctx,
            String,
            String,
            llm::Request,
            CallFn,
        ) -> BoxFuture<'static, Result<llm::Response, llm::Error>>
        + Send
        + Sync,
>;

/// The future of a provider embedding call, as an instrumentation wraps it.
pub type EmbedCallFn = Box<
    dyn FnOnce(Ctx) -> BoxFuture<'static, Result<llm::EmbedResponse, llm::Error>> + Send + 'static,
>;

/// InstrumentEmbed wraps every provider embedding call: context, provider, model, the number of texts and the call.
pub type InstrumentEmbed = Arc<
    dyn Fn(
            Ctx,
            String,
            String,
            usize,
            EmbedCallFn,
        ) -> BoxFuture<'static, Result<llm::EmbedResponse, llm::Error>>
        + Send
        + Sync,
>;

#[derive(Default)]
struct Routes {
    providers: HashMap<String, Arc<dyn Provider>>,
    aliases: HashMap<String, Target>,
}

/// Router resolves aliases and dispatches to providers.
#[derive(Default)]
pub struct Router {
    /// Instrument wraps every provider call (OpenTelemetry GenAI spans and metrics).
    pub instrument: Option<Instrument>,
    /// InstrumentEmbed wraps every provider embedding call.
    pub instrument_embed: Option<InstrumentEmbed>,
    /// Retries bounds the retries of a transient provider failure (retry.rs): 0 is DEFAULT_RETRIES, negative none.
    pub retries: i64,
    /// RetryBase is the first backoff (zero: one second); the next ones double it, with jitter.
    pub retry_base: Duration,
    routes: RwLock<Routes>,
}

impl Router {
    /// An empty router.
    pub fn new() -> Self {
        Router::default()
    }

    /// AddProvider registers a provider.
    pub fn add_provider(&self, name: &str, p: Arc<dyn Provider>) {
        self.routes.write().providers.insert(name.to_string(), p);
    }

    /// Replace swaps every provider and alias at once (configuration reload).
    pub fn replace(
        &self,
        providers: HashMap<String, Arc<dyn Provider>>,
        aliases: HashMap<String, Target>,
    ) {
        *self.routes.write() = Routes { providers, aliases };
    }

    /// SetAlias maps an alias to "provider/model".
    pub fn set_alias(&self, alias: &str, target: &str) -> Result<(), Error> {
        let t = parse_target(target).ok_or_else(|| {
            Error::Message(format!(
                "alias {alias}: target {target:?} must be provider/model"
            ))
        })?;
        self.routes.write().aliases.insert(alias.to_string(), t);
        Ok(())
    }

    /// Resolve returns the provider target of a model name.
    pub fn resolve(&self, model: &str) -> Result<(Target, Arc<dyn Provider>), llm::Error> {
        let routes = self.routes.read();
        let model = if model.is_empty() { "default" } else { model };
        let t = match routes.aliases.get(model) {
            Some(t) => t.clone(),
            None => parse_target(model).ok_or_else(|| {
                llm::Error::Message(format!("unknown model alias {}", quote(model)))
            })?,
        };
        let p = routes.providers.get(&t.provider).cloned().ok_or_else(|| {
            llm::Error::Message(format!("unknown provider {}", quote(&t.provider)))
        })?;
        Ok((t, p))
    }

    /// Complete implements llm::Client.
    pub async fn complete(
        &self,
        ctx: &Ctx,
        req: llm::Request,
    ) -> Result<llm::Response, llm::Error> {
        let (t, p) = self.resolve(&req.model)?;
        let policy: RetryPolicy = self.policy();
        let target = t.clone();
        let sent = req.clone();
        let call: CallFn = Box::new(move |ctx: Ctx| {
            Box::pin(async move {
                let mut resp = complete_tools(&ctx, policy, &target, p.as_ref(), req).await?;
                if resp.provider.is_empty() {
                    resp.provider = target.provider.clone();
                }
                if resp.model.is_empty() {
                    resp.model = target.model.clone();
                }
                Ok(resp)
            })
        });
        let res = match &self.instrument {
            Some(i) => i(ctx.clone(), t.provider.clone(), t.model.clone(), sent, call).await,
            None => call(ctx.clone()).await,
        };
        res.map_err(|e| e.wrap(format!("{}/{}", t.provider, t.model)))
    }

    /// Aliases lists the aliases, sorted, their targets and the providers, sorted.
    pub fn aliases(&self) -> (Vec<String>, HashMap<String, Target>, Vec<String>) {
        let routes = self.routes.read();
        let mut names: Vec<String> = routes.aliases.keys().cloned().collect();
        names.sort();
        let targets = routes.aliases.clone();
        let mut providers: Vec<String> = routes.providers.keys().cloned().collect();
        providers.sort();
        (names, targets, providers)
    }
}

#[async_trait]
impl llm::Client for Router {
    async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, llm::Error> {
        Router::complete(self, ctx, req).await
    }
}

/// Go's `%q` of a short text.
pub(crate) fn quote(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_else(|_| format!("\"{s}\""))
}

/// Config is the gateway configuration file.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Config {
    pub providers: BTreeMap<String, ProviderConfig>,
    pub aliases: BTreeMap<String, String>,
}

/// ProviderConfig configures one provider.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ProviderConfig {
    /// anthropic | openai | fake
    #[serde(rename = "type")]
    pub r#type: String,
    #[serde(rename = "baseURL", skip_serializing_if = "String::is_empty")]
    pub base_url: String,
    /// APIKeySecret is a Vault reference "<path>#<field>"; APIKeyEnv the fallback.
    #[serde(rename = "apiKeySecret", skip_serializing_if = "String::is_empty")]
    pub api_key_secret: String,
    #[serde(rename = "apiKeyEnv", skip_serializing_if = "String::is_empty")]
    pub api_key_env: String,
}

/// LoadConfig reads a YAML config file.
pub fn load_config(path: &str) -> Result<Config, Error> {
    let data =
        std::fs::read_to_string(path).map_err(|e| Error::Message(format!("open {path}: {e}")))?;
    serde_yaml_ng::from_str(&data).map_err(|e| Error::Message(e.to_string()))
}

/// DefaultConfig uses Anthropic when a key is available, the fake provider otherwise.
pub fn default_config(has_anthropic_key: bool) -> Config {
    let mut c = Config::default();
    c.providers.insert(
        "fake".into(),
        ProviderConfig {
            r#type: "fake".into(),
            ..Default::default()
        },
    );
    c.providers.insert(
        "anthropic".into(),
        ProviderConfig {
            r#type: "anthropic".into(),
            api_key_secret: "goap/modelgw#anthropic_api_key".into(),
            api_key_env: "ANTHROPIC_API_KEY".into(),
            ..Default::default()
        },
    );
    if has_anthropic_key {
        c.aliases
            .insert("default".into(), "anthropic/claude-opus-5".into());
        c.aliases
            .insert("fast".into(), "anthropic/claude-haiku-4-5".into());
    } else {
        // "embed" is the embedding model of the node index (ADR 0026): the fake one hashes words, enough for dev
        c.aliases.insert("default".into(), "fake/echo".into());
        c.aliases.insert("fast".into(), "fake/echo".into());
        c.aliases
            .insert(llm::EMBED_ALIAS.into(), "fake/hash".into());
    }
    c
}

/// The graph objects a configuration seeds: providers (the key by reference), the models the aliases point to, and
/// the aliases.
pub type Objects = (
    Vec<llmcfg::Provider>,
    Vec<llmcfg::Model>,
    Vec<llmcfg::Alias>,
);

impl Config {
    /// Objects returns the configuration as the graph objects it seeds: providers (the key by reference), the
    /// models the aliases point to, and the aliases. Aliases of a provider that is not configured are left out.
    pub fn objects(&self) -> Result<Objects, Error> {
        let mut provs = Vec::new();
        let mut have = HashSet::new();
        for (name, pc) in &self.providers {
            let kind = if pc.r#type == "openai" {
                "openai-compatible"
            } else {
                pc.r#type.as_str()
            };
            let k = lookup_kind(kind).ok_or_else(|| {
                Error::Message(format!(
                    "provider {name}: unknown type {}",
                    quote(&pc.r#type)
                ))
            })?;
            let mut refs = Vec::new();
            if !pc.api_key_secret.is_empty() {
                refs.push(pc.api_key_secret.clone());
            }
            if !pc.api_key_env.is_empty() {
                refs.push(format!("env:{}", pc.api_key_env));
            }
            provs.push(llmcfg::Provider {
                name: name.clone(),
                kind: k.id.clone(),
                protocol: k.protocol.clone(),
                base_url: pc.base_url.clone(),
                enabled: true,
                api_key_ref: refs.join(" | "),
            });
            have.insert(name.clone());
        }
        let mut aliases = Vec::new();
        let mut models = Vec::new();
        let mut seen = HashSet::new();
        for (a, t) in &self.aliases {
            let Some((p, m)) = llmcfg::split_target(t) else {
                continue;
            };
            if !have.contains(p) {
                continue;
            }
            aliases.push(llmcfg::Alias {
                alias: a.clone(),
                target: t.clone(),
                protected: false,
            });
            if seen.insert(t.clone()) {
                models.push(llmcfg::Model {
                    provider: p.to_string(),
                    model: m.to_string(),
                    display_name: m.to_string(),
                    enabled: true,
                    quota_period: llmcfg::PERIOD_MONTH.to_string(),
                    ..Default::default()
                });
            }
        }
        Ok((provs, models, aliases))
    }
}

/// SecretFunc resolves a secret (Vault reference, env fallback).
pub type SecretFunc =
    Arc<dyn Fn(Ctx, String, String) -> BoxFuture<'static, Result<String, BoxError>> + Send + Sync>;

/// Build creates a router from a config.
pub async fn build(ctx: &Ctx, c: &Config, secret: SecretFunc) -> Result<Router, Error> {
    let r = Router::new();
    for (name, pc) in &c.providers {
        let mut key = String::new();
        if !pc.api_key_secret.is_empty() || !pc.api_key_env.is_empty() {
            key = secret(
                ctx.clone(),
                pc.api_key_secret.clone(),
                pc.api_key_env.clone(),
            )
            .await
            .map_err(|e| Error::Message(format!("provider {name}: {e}")))?;
        }
        match pc.r#type.as_str() {
            "anthropic" => {
                if key.is_empty() {
                    continue; // not configured
                }
                r.add_provider(name, Arc::new(Anthropic::new(&key, &pc.base_url)));
            }
            "openai" => r.add_provider(
                name,
                Arc::new(OpenAICompatible {
                    base_url: pc.base_url.clone(),
                    api_key: key,
                    http: None,
                }),
            ),
            "fake" => r.add_provider(name, Arc::new(Fake)),
            _ => {
                return Err(Error::Message(format!(
                    "provider {name}: unknown type {}",
                    quote(&pc.r#type)
                )));
            }
        }
    }
    for (a, t) in &c.aliases {
        r.set_alias(a, t)?;
    }
    Ok(r)
}
