//! Providers, models, aliases and the snapshot of the configuration.

use std::fmt::Write as _;
use std::sync::{Arc, LazyLock};
use std::time::Duration;

use goap_ctx::Ctx;
use goap_domain::{BaselineID, Link, Node};
use goap_graphsnap::{Cache, Graph, ReadError};
use regex::Regex;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{Behavior, NODE_TYPE_BEHAVIOR, behavior_from_props};

/// The errors of the package.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A configuration entry breaks a rule (the Go message).
    #[error("{0}")]
    Invalid(String),
    /// The properties of a node do not read as its entry (`"<what> node: <cause>"`).
    #[error("{what} node: {source}")]
    Decode {
        what: &'static str,
        #[source]
        source: serde_json::Error,
    },
}

impl Error {
    pub(crate) fn invalid(msg: impl Into<String>) -> Self {
        Error::Invalid(msg.into())
    }
}

/// The namespace of the graph objects.
pub const NAMESPACE_PLATFORM: &str = goap_domain::NAMESPACE_PLATFORM;
pub const NODE_TYPE_PROVIDER: &str = "platform@LlmProvider";
pub const NODE_TYPE_MODEL: &str = "platform@LlmModel";
pub const NODE_TYPE_ALIAS: &str = "platform@LlmAlias";
/// StateRetired is the state of an entry taken out of the configuration (lifecycle config of the platform domain,
/// ADR 0076: a node is never deleted): the snapshot leaves it out.
pub const STATE_RETIRED: &str = "retired";

// Quota periods.
pub const PERIOD_DAY: &str = "day";
pub const PERIOD_MONTH: &str = "month";
pub const PERIOD_TOTAL: &str = "total";

static NAME_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[a-z0-9][a-z0-9_-]{0,39}$").expect("name pattern"));

/// ValidName reports whether s can name a provider or an alias.
pub fn valid_name(s: &str) -> bool {
    NAME_RE.is_match(s)
}

fn is_false(b: &bool) -> bool {
    !*b
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

/// Provider is an LLM provider: a kind (preset) on a wire protocol. APIKeyRef references the API key
/// ("env:<VAR>" or "<vault path>#<field>", alternatives separated by "|"); the key itself is never stored.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Provider {
    pub name: String,
    pub kind: String,
    pub protocol: String,
    #[serde(rename = "baseURL", skip_serializing_if = "String::is_empty")]
    pub base_url: String,
    pub enabled: bool,
    #[serde(rename = "apiKeyRef", skip_serializing_if = "String::is_empty")]
    pub api_key_ref: String,
}

/// Model is a model offered on the platform.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Model {
    pub provider: String,
    pub model: String,
    #[serde(rename = "displayName", skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    pub enabled: bool,
    /// QuotaTokens is the global token budget per QuotaPeriod (0: unlimited).
    #[serde(rename = "quotaTokens", skip_serializing_if = "is_zero")]
    pub quota_tokens: i64,
    /// day | month | total
    #[serde(rename = "quotaPeriod", skip_serializing_if = "String::is_empty")]
    pub quota_period: String,
    /// Roles may call the model (empty: every authenticated caller).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub roles: Vec<String>,
}

// The protected aliases of the platform (ADR 0084): internal names the platform itself resolves, so they exist on every
// install and cannot be retired or renamed, only retargeted.

/// AssistantAlias is the model of the conversational assistant.
pub const ASSISTANT_ALIAS: &str = "assistant";
/// HelperAlias is the model of the contextual helper that fills fields.
pub const HELPER_ALIAS: &str = "helper";

/// ProtectedAliases lists the aliases the platform keeps on every install.
pub fn protected_aliases() -> Vec<&'static str> {
    vec![ASSISTANT_ALIAS, HELPER_ALIAS]
}

/// Alias maps a name ("default", "fast") to "provider/model".
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Alias {
    pub alias: String,
    /// Target is "provider/model"; empty only for a protected alias nothing is configured for yet (it then resolves to
    /// nothing, so it is not available).
    pub target: String,
    /// Protected marks an alias the platform itself uses: it may not be retired nor renamed (ADR 0084).
    #[serde(skip_serializing_if = "is_false")]
    pub protected: bool,
}

/// ProviderKey is the key of the node of a provider.
pub fn provider_key(name: &str) -> String {
    format!("LLP:{name}")
}

/// ModelKey is the key of the node of a model; it also keys the token usage of the model.
pub fn model_key(provider: &str, model: &str) -> String {
    format!("LLM:{provider}/{model}")
}

/// AliasKey is the key of the node of an alias.
pub fn alias_key(alias: &str) -> String {
    format!("LLA:{alias}")
}

/// SplitTarget splits "provider/model" (None unless both are set).
pub fn split_target(target: &str) -> Option<(&str, &str)> {
    match target.split_once('/') {
        Some((p, m)) if !p.is_empty() && !m.is_empty() => Some((p, m)),
        _ => None,
    }
}

impl Provider {
    /// Validate checks a provider.
    pub fn validate(&self) -> Result<(), Error> {
        if !valid_name(&self.name) {
            return Err(Error::invalid(
                "provider name must be 1-40 characters of a-z, 0-9, - or _",
            ));
        }
        if self.protocol.is_empty() {
            return Err(Error::invalid(format!(
                "provider {}: protocol required",
                self.name
            )));
        }
        if !self.base_url.is_empty()
            && !self.base_url.starts_with("http://")
            && !self.base_url.starts_with("https://")
        {
            return Err(Error::invalid(format!(
                "provider {}: base URL must start with http:// or https://",
                self.name
            )));
        }
        Ok(())
    }

    /// Props returns the properties of the LlmProvider node.
    pub fn props(&self) -> Map<String, Value> {
        to_props(self)
    }
}

impl Model {
    /// Key is the key of the node of the model.
    pub fn key(&self) -> String {
        model_key(&self.provider, &self.model)
    }

    /// Validate checks a model.
    pub fn validate(&self) -> Result<(), Error> {
        if self.provider.is_empty() || self.model.is_empty() {
            return Err(Error::invalid("model needs a provider and a model id"));
        }
        if self.quota_tokens < 0 {
            return Err(Error::invalid(format!(
                "model {}/{}: quota must be positive (0: unlimited)",
                self.provider, self.model
            )));
        }
        match self.quota_period.as_str() {
            "" | PERIOD_DAY | PERIOD_MONTH | PERIOD_TOTAL => Ok(()),
            _ => Err(Error::invalid(format!(
                "model {}/{}: quota period must be day, month or total",
                self.provider, self.model
            ))),
        }
    }

    fn defaults(&mut self) {
        if self.quota_period.is_empty() {
            self.quota_period = PERIOD_MONTH.to_string();
        }
        if self.display_name.is_empty() {
            self.display_name = self.model.clone();
        }
    }

    /// Props returns the properties of the LlmModel node.
    pub fn props(&self) -> Map<String, Value> {
        let mut m = self.clone();
        m.defaults();
        to_props(&m)
    }
}

impl Alias {
    /// Validate checks an alias.
    pub fn validate(&self) -> Result<(), Error> {
        if !valid_name(&self.alias) {
            return Err(Error::invalid(
                "alias must be 1-40 characters of a-z, 0-9, - or _",
            ));
        }
        // the target (provider/model, except for a protected alias) is the domain's rule: the node validator
        // alias-needs-target of platform@LlmAlias, enforced at review and at landing.
        Ok(())
    }

    /// Props returns the properties of the LlmAlias node.
    pub fn props(&self) -> Map<String, Value> {
        to_props(self)
    }
}

pub(crate) fn to_props<T: Serialize>(v: &T) -> Map<String, Value> {
    match serde_json::to_value(v) {
        Ok(Value::Object(m)) => m,
        _ => Map::new(),
    }
}

/// Reads properties the way Go's encoding/json does: a null leaves the field at its zero value, an integral float
/// reads as an integer.
pub(crate) fn from_props<T: DeserializeOwned>(props: &Map<String, Value>) -> serde_json::Result<T> {
    fn norm(v: &Value) -> Value {
        match v {
            Value::Number(n) if n.as_i64().is_none() && n.as_u64().is_none() => match n.as_f64() {
                Some(f) if f.fract() == 0.0 && f.abs() < 9.0e15 => Value::from(f as i64),
                _ => v.clone(),
            },
            Value::Array(a) => Value::Array(a.iter().map(norm).collect()),
            Value::Object(m) => Value::Object(
                m.iter()
                    .filter(|(_, v)| !v.is_null())
                    .map(|(k, v)| (k.clone(), norm(v)))
                    .collect(),
            ),
            _ => v.clone(),
        }
    }
    serde_json::from_value(norm(&Value::Object(props.clone())))
}

/// ProviderFromProps reads a provider from the properties of its node.
pub fn provider_from_props(props: &Map<String, Value>) -> Result<Provider, Error> {
    let p: Provider = from_props(props).map_err(|source| Error::Decode {
        what: "provider",
        source,
    })?;
    p.validate()?;
    Ok(p)
}

/// ModelFromProps reads a model from the properties of its node.
pub fn model_from_props(props: &Map<String, Value>) -> Result<Model, Error> {
    let mut m: Model = from_props(props).map_err(|source| Error::Decode {
        what: "model",
        source,
    })?;
    m.defaults();
    m.validate()?;
    Ok(m)
}

/// AliasFromProps reads an alias from the properties of its node.
pub fn alias_from_props(props: &Map<String, Value>) -> Result<Alias, Error> {
    let a: Alias = from_props(props).map_err(|source| Error::Decode {
        what: "alias",
        source,
    })?;
    a.validate()?;
    Ok(a)
}

/// Snapshot is the gateway configuration as of one baseline.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Snapshot {
    pub baseline: BaselineID,
    /// Problems lists the nodes that could not be read or that point to nothing.
    pub problems: Vec<String>,
    pub providers: Vec<Provider>,
    pub models: Vec<Model>,
    pub aliases: Vec<Alias>,
    /// Behaviors are the global behaviours in force (ADR 0093: enabled, not retired); Off are the disabled ones, kept for
    /// the administrators' listing.
    pub behaviors: Vec<Behavior>,
    pub off: Vec<Behavior>,
}

/// BuildSnapshot reads the configuration of a baseline graph. Models of an unknown provider and aliases
/// of an unknown model are left out and reported.
pub fn build_snapshot(id: &BaselineID, nodes: &[Node], _links: &[Link]) -> Snapshot {
    let mut s = Snapshot {
        baseline: id.clone(),
        ..Default::default()
    };
    for n in nodes {
        // nodes is scoped to the platform namespace by the Directory's cache
        // (namespace: NAMESPACE_PLATFORM); no need to filter it again here.
        if n.state == STATE_RETIRED {
            continue;
        }
        let res = match n.r#type.as_str() {
            NODE_TYPE_PROVIDER => provider_from_props(&n.properties).map(|p| s.providers.push(p)),
            NODE_TYPE_MODEL => model_from_props(&n.properties).map(|m| s.models.push(m)),
            NODE_TYPE_ALIAS => alias_from_props(&n.properties).map(|a| s.aliases.push(a)),
            NODE_TYPE_BEHAVIOR => behavior_from_props(&n.properties).map(|b| {
                if b.enabled {
                    s.behaviors.push(b)
                } else {
                    s.off.push(b)
                }
            }),
            _ => Ok(()),
        };
        if let Err(err) = res {
            s.problems.push(format!("{}: {err}", n.key));
        }
    }
    let prov: std::collections::HashSet<&str> =
        s.providers.iter().map(|p| p.name.as_str()).collect();
    let mut have = std::collections::HashSet::new();
    let mut keep = Vec::new();
    for m in std::mem::take(&mut s.models) {
        if !prov.contains(m.provider.as_str()) {
            s.problems
                .push(format!("{}: unknown provider {}", m.key(), m.provider));
            continue;
        }
        have.insert(format!("{}/{}", m.provider, m.model));
        keep.push(m);
    }
    s.models = keep;
    let mut keep_a = Vec::new();
    for a in std::mem::take(&mut s.aliases) {
        if a.protected && !have.contains(&a.target) {
            // nothing is configured for it (yet): it stays listed, resolving to nothing, and is no problem
            keep_a.push(a);
            continue;
        }
        if !have.contains(&a.target) {
            let mut p = String::new();
            let _ = write!(p, "{}: unknown model {}", alias_key(&a.alias), a.target);
            s.problems.push(p);
            continue;
        }
        keep_a.push(a);
    }
    s.aliases = keep_a;
    s.providers.sort_by(|a, b| a.name.cmp(&b.name));
    s.models.sort_by_key(Model::key);
    s.aliases.sort_by(|a, b| a.alias.cmp(&b.alias));
    let by_name = |l: &mut Vec<Behavior>| {
        l.sort_by(|a, b| a.order.cmp(&b.order).then_with(|| a.name.cmp(&b.name)));
    };
    by_name(&mut s.behaviors);
    by_name(&mut s.off);
    s
}

/// Directory reads the snapshot of the head of the main branch (see goap_graphsnap::Cache).
pub struct Directory {
    cache: Cache<Arc<Snapshot>>,
}

impl Directory {
    /// A directory over a graph, looking at the head once per second.
    pub fn new(graph: Arc<dyn Graph>) -> Self {
        Self {
            cache: Cache::new(
                graph,
                NAMESPACE_PLATFORM,
                |id: &BaselineID, n: &[Node], l: &[Link]| Arc::new(build_snapshot(id, n, l)),
            ),
        }
    }

    /// The directory looking at the head once per `ttl` (zero: one second).
    pub fn with_ttl(mut self, ttl: Duration) -> Self {
        self.cache.ttl = ttl;
        self
    }

    /// Snapshot returns the current snapshot; the last one keeps serving when the graph cannot be read (in the
    /// error).
    pub async fn snapshot(&self, ctx: &Ctx) -> Result<Arc<Snapshot>, ReadError<Arc<Snapshot>>> {
        self.cache.get(ctx).await.map(|(s, _)| s)
    }
}

/// Go's `strconv.Quote` (`%q`) of the short texts the messages quote.
pub(crate) fn go_quote(s: &str) -> String {
    let mut b = String::with_capacity(s.len() + 2);
    b.push('"');
    for c in s.chars() {
        match c {
            '"' => b.push_str("\\\""),
            '\\' => b.push_str("\\\\"),
            '\u{7}' => b.push_str("\\a"),
            '\u{8}' => b.push_str("\\b"),
            '\u{c}' => b.push_str("\\f"),
            '\n' => b.push_str("\\n"),
            '\r' => b.push_str("\\r"),
            '\t' => b.push_str("\\t"),
            '\u{b}' => b.push_str("\\v"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => {
                let _ = write!(b, "\\x{:02x}", c as u32);
            }
            c if c.is_control() || (c != ' ' && c.is_whitespace()) => {
                let _ = write!(b, "\\u{:04x}", c as u32);
            }
            c => b.push(c),
        }
    }
    b.push('"');
    b
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use serde_json::json;

    pub(crate) fn node(key: &str, typ: &str, props: Map<String, Value>) -> Node {
        Node {
            namespace: NAMESPACE_PLATFORM.into(),
            key: key.into(),
            r#type: typ.into(),
            properties: props,
            ..Default::default()
        }
    }

    #[test]
    fn test_props_round_trip() {
        let m = Model {
            provider: "mistral".into(),
            model: "large".into(),
            enabled: true,
            quota_tokens: 1000,
            quota_period: PERIOD_DAY.into(),
            roles: vec!["methodologist".into()],
            ..Default::default()
        };
        let got = model_from_props(&m.props()).unwrap();
        assert!(
            got.quota_tokens == 1000
                && got.quota_period == PERIOD_DAY
                && got.roles.len() == 1
                && got.display_name == "large",
            "{got:?}"
        );
        let p = Provider {
            name: "mistral".into(),
            kind: "mistral".into(),
            protocol: "openai".into(),
            enabled: true,
            api_key_ref: "env:K".into(),
            ..Default::default()
        };
        assert_eq!(provider_from_props(&p.props()).unwrap(), p);
    }

    #[test]
    fn test_snapshot_ignores_dangling_and_malformed_nodes() {
        let prov = Provider {
            name: "fake".into(),
            kind: "fake".into(),
            protocol: "fake".into(),
            enabled: true,
            ..Default::default()
        };
        let ok = Model {
            provider: "fake".into(),
            model: "echo".into(),
            enabled: true,
            ..Default::default()
        };
        let orphan = Model {
            provider: "gone".into(),
            model: "x".into(),
            enabled: true,
            ..Default::default()
        };
        let alias = |a: &str, t: &str| Alias {
            alias: a.into(),
            target: t.into(),
            protected: false,
        };
        let s = build_snapshot(
            &"b1".into(),
            &[
                node(&provider_key("fake"), NODE_TYPE_PROVIDER, prov.props()),
                node(&ok.key(), NODE_TYPE_MODEL, ok.props()),
                node(&orphan.key(), NODE_TYPE_MODEL, orphan.props()),
                node(
                    &alias_key("default"),
                    NODE_TYPE_ALIAS,
                    alias("default", "fake/echo").props(),
                ),
                node(
                    &alias_key("lost"),
                    NODE_TYPE_ALIAS,
                    alias("lost", "fake/none").props(),
                ),
                node(
                    &provider_key("Bad Name"),
                    NODE_TYPE_PROVIDER,
                    json!({"name": "Bad Name", "protocol": "fake"})
                        .as_object()
                        .cloned()
                        .unwrap(),
                ),
            ],
            &[],
        );
        assert!(
            s.providers.len() == 1
                && s.models.len() == 1
                && s.aliases.len() == 1
                && s.problems.len() == 3,
            "{s:?}"
        );
    }

    #[test]
    fn test_protected_alias_props() {
        let a = Alias {
            alias: ASSISTANT_ALIAS.into(),
            target: "fake/echo".into(),
            protected: true,
        };
        assert_eq!(alias_from_props(&a.props()).unwrap(), a);
        let plain = Alias {
            alias: "x".into(),
            target: "a/b".into(),
            protected: false,
        };
        assert!(
            !plain.props().contains_key("protected"),
            "an unprotected alias carries no flag"
        );
        // the target rule is the domain's (validator alias-needs-target of platform@LlmAlias, tested in graphsvc): reading an
        // alias checks its name only
        let helper = Alias {
            alias: HELPER_ALIAS.into(),
            protected: true,
            ..Default::default()
        };
        alias_from_props(&helper.props()).unwrap();
        let bad = Alias {
            alias: "Bad Name".into(),
            ..Default::default()
        };
        assert!(
            alias_from_props(&bad.props()).is_err(),
            "an alias needs a valid name"
        );
        // an unprotected alias with no target cannot land; read anyway it resolves to no model and is reported as such
        let x = Alias {
            alias: "x".into(),
            ..Default::default()
        };
        let s0 = build_snapshot(
            &"".into(),
            &[node(&alias_key("x"), NODE_TYPE_ALIAS, x.props())],
            &[],
        );
        assert!(s0.aliases.is_empty() && s0.problems.len() == 1, "{s0:?}");
        // a protected alias whose model is gone stays in the snapshot, no problem
        let gone = Alias {
            alias: HELPER_ALIAS.into(),
            target: "gone/m".into(),
            protected: true,
        };
        let s = build_snapshot(
            &"".into(),
            &[node(
                &alias_key(HELPER_ALIAS),
                NODE_TYPE_ALIAS,
                gone.props(),
            )],
            &[],
        );
        assert!(s.aliases.len() == 1 && s.problems.is_empty(), "{s:?}");
    }

    #[test]
    fn test_from_props_reads_like_go() {
        let m = model_from_props(
            &json!({"provider": "p", "model": "m", "quotaTokens": 1000.0, "displayName": null})
                .as_object()
                .cloned()
                .unwrap(),
        )
        .unwrap();
        assert_eq!((m.quota_tokens, m.display_name.as_str()), (1000, "m"));
        let err =
            model_from_props(&json!({"provider": 3}).as_object().cloned().unwrap()).unwrap_err();
        assert!(err.to_string().starts_with("model node: "), "{err}");
    }
}
