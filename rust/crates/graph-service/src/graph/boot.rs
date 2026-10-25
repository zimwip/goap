//! The platform bootstrap (Go's `graphsvc.Boot`, ADR 0071): the roots of the structures, then the seeds, each its own
//! change on main in the root project, each idempotent, so a second run writes nothing. What the seeds write is the
//! Go code's (`boot.gen.json`, written by `platform/devseed` `TestRustBootIsCurrent`).
use super::*;
use graph_core::{gojson, model::Node, sources::Source, view::View};
use serde::Deserialize;
use serde_json::{Map, Value as Json};

/// The nodes of the seeds, as the Go code states them.
const BOOT_JSON: &str = include_str!("../boot.gen.json");

/// A node a seed writes.
#[derive(Debug, Clone, Deserialize)]
pub struct SeedNode {
    pub key: String,
    #[serde(rename = "type")]
    pub typ: String,
    pub props: Map<String, Json>,
    #[serde(default)]
    pub owner: String,
}

impl SeedNode {
    /// Its creation (`graphsvc.SeedNode`).
    fn edit(&self) -> NodeEdit {
        NodeEdit {
            key: self.key.clone(),
            typ: self.typ.clone(),
            props: Some(self.props.clone()),
            rationale: format!("Seed {}", self.key),
            owner: self.owner.clone(),
            ..NodeEdit::default()
        }
    }
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Seeds {
    floor_policy: String,
    policies: Vec<SeedNode>,
    builtins: Vec<SeedNode>,
    #[serde(rename = "builtinMCPs")]
    builtin_mcps: Vec<String>,
    adapters: HashMap<String, SeedNode>,
    behavior: SeedNode,
    protected_aliases: Vec<String>,
    platform_source: SeedNode,
    document_repository: Vec<SeedNode>,
    model_kinds: HashMap<String, ModelKind>,
    #[cfg_attr(not(test), allow(dead_code))]
    models: Vec<ModelCase>,
}

#[derive(Debug, Deserialize)]
struct ModelKind {
    id: String,
    protocol: String,
}

#[derive(Debug, Deserialize)]
#[cfg_attr(not(test), allow(dead_code))]
struct ModelCase {
    config: ModelsConfig,
    nodes: Vec<SeedNode>,
}

fn seeds() -> Seeds {
    // the file is generated from the Go code and checked by its test: it always reads
    serde_json::from_str(BOOT_JSON).expect("boot.gen.json")
}

/// The configuration of the model gateway (`modelgw.Config`, the file of `GOAP_MODELS_CONFIG`).
#[derive(Debug, Clone, Default, Deserialize)]
pub struct ModelsConfig {
    #[serde(default)]
    pub providers: std::collections::BTreeMap<String, ProviderConfig>,
    #[serde(default)]
    pub aliases: std::collections::BTreeMap<String, String>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ProviderConfig {
    #[serde(rename = "type", default)]
    pub typ: String,
    #[serde(rename = "baseURL", default)]
    pub base_url: String,
    #[serde(rename = "apiKeySecret", default)]
    pub api_key_secret: String,
    #[serde(rename = "apiKeyEnv", default)]
    pub api_key_env: String,
}

impl ModelsConfig {
    /// The default (`modelgw.DefaultConfig`): Anthropic when a key is available, the fake provider otherwise.
    pub fn default_for(has_anthropic_key: bool) -> ModelsConfig {
        let provider = |typ: &str, secret: &str, env: &str| ProviderConfig {
            typ: typ.into(),
            api_key_secret: secret.into(),
            api_key_env: env.into(),
            ..ProviderConfig::default()
        };
        let mut c = ModelsConfig::default();
        c.providers.insert("fake".into(), provider("fake", "", ""));
        c.providers.insert(
            "anthropic".into(),
            provider("anthropic", "goap/modelgw#anthropic_api_key", "ANTHROPIC_API_KEY"),
        );
        let aliases: &[(&str, &str)] = if has_anthropic_key {
            &[
                ("default", "anthropic/claude-opus-5"),
                ("fast", "anthropic/claude-haiku-4-5"),
            ]
        } else {
            // "embed" is the embedding model of the node index (ADR 0026)
            &[("default", "fake/echo"), ("fast", "fake/echo"), ("embed", "fake/hash")]
        };
        c.aliases = aliases.iter().map(|(a, t)| (a.to_string(), t.to_string())).collect();
        c
    }

    /// The configuration the graph is seeded with (`modelgw.InitialConfig`): the file at `path`, else the default,
    /// Anthropic when its key is a secret (`goap/modelgw#anthropic_api_key` in Vault, else `ANTHROPIC_API_KEY`).
    pub async fn initial(path: &str) -> anyhow::Result<ModelsConfig> {
        if path.is_empty() {
            let key = secret("goap/modelgw#anthropic_api_key", "ANTHROPIC_API_KEY").await;
            return Ok(ModelsConfig::default_for(!key.is_empty()));
        }
        let src = std::fs::read_to_string(path)?;
        Ok(serde_norway::from_str(&src)?)
    }

    /// The nodes it seeds (`Config.Objects`): the providers by name (the key by reference), the models the aliases
    /// point to, then the aliases by name; an alias of a provider not configured is left out.
    pub fn nodes(&self) -> Result<Vec<SeedNode>, String> {
        let kinds = seeds().model_kinds;
        let node = |key: String, typ: &str, props: Json| SeedNode {
            key,
            typ: typ.into(),
            props: match props {
                Json::Object(m) => m,
                _ => Map::new(),
            },
            owner: String::new(),
        };
        let mut out = Vec::new();
        for (name, pc) in &self.providers {
            let kind = if pc.typ == "openai" {
                "openai-compatible"
            } else {
                &pc.typ
            };
            let Some(k) = kinds.get(kind) else {
                return Err(format!("provider {name}: unknown type {}", goyaml_quote(&pc.typ)));
            };
            let refs: Vec<String> = [
                pc.api_key_secret.clone(),
                if pc.api_key_env.is_empty() {
                    String::new()
                } else {
                    format!("env:{}", pc.api_key_env)
                },
            ]
            .into_iter()
            .filter(|r| !r.is_empty())
            .collect();
            let mut p = Map::new();
            p.insert("name".into(), name.as_str().into());
            p.insert("kind".into(), k.id.as_str().into());
            p.insert("protocol".into(), k.protocol.as_str().into());
            if !pc.base_url.is_empty() {
                p.insert("baseURL".into(), pc.base_url.as_str().into());
            }
            p.insert("enabled".into(), true.into());
            if !refs.is_empty() {
                p.insert("apiKeyRef".into(), refs.join(" | ").into());
            }
            out.push(node(format!("LLP:{name}"), "platform@LlmProvider", Json::Object(p)));
        }
        let mut models = Vec::new();
        let mut aliases = Vec::new();
        let mut seen = std::collections::HashSet::new();
        for (a, t) in &self.aliases {
            let Some((p, m)) = t.split_once('/').filter(|(p, m)| !p.is_empty() && !m.is_empty()) else {
                continue;
            };
            if !self.providers.contains_key(p) {
                continue;
            }
            aliases.push(node(
                format!("LLA:{a}"),
                "platform@LlmAlias",
                serde_json::json!({"alias": a, "target": t}),
            ));
            if seen.insert(t.clone()) {
                models.push(node(
                    format!("LLM:{p}/{m}"),
                    "platform@LlmModel",
                    serde_json::json!({"provider": p, "model": m, "displayName": m, "enabled": true,
                        "quotaPeriod": "month"}),
                ));
            }
        }
        out.extend(models);
        out.extend(aliases);
        Ok(out)
    }
}

/// A text quoted as Go's `%q` writes it.
fn goyaml_quote(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

/// What the dev seed of the composition adds (`GOAP_GRAPH_SEED`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum DevSeed {
    #[default]
    None,
    /// The development data: the document-repository MCP.
    Dev,
    /// The demo too: the organisation `ORG-ACME`, the project `PROJ-DEMO`, its ALM data.
    Demo,
}

/// What differs between the boots (Go's `graphsvc.Options`).
#[derive(Debug, Clone, Default)]
pub struct BootOptions {
    /// The model gateway configuration seeded when the graph holds no provider yet.
    pub models: ModelsConfig,
    /// The graph sources declared besides the platform graph's own.
    pub sources: Vec<Source>,
    pub dev: DevSeed,
}

/// What the boot wrote (Go's `graphsvc.Report`).
#[derive(Debug, Clone, Default)]
pub struct Report {
    pub roots: bool,
    pub access: bool,
    pub builtins: bool,
    pub models: bool,
    pub aliases: bool,
    pub behaviors: bool,
    pub sources: bool,
    pub dev: bool,
}

/// The graph service acting by itself, as the seeds do (`graphsvc.System`).
fn system() -> Caller {
    Caller {
        subject: SYSTEM_GRAPH.into(),
        on_behalf_of: String::new(),
        at_least: Vec::new(),
        project: String::new(),
        command: String::new(),
        roles: vec![ROLE_ADMIN.into()],
        org: String::new(),
    }
}

/// Commits the edits of a seed on main as one change of a namespace in the root project (`graphsvc.SeedChange`):
/// false when the graph holds what they state already.
async fn seed_change(
    tx: &mut Tx<'_, Session>,
    namespace: &str,
    title: &str,
    edits: Vec<NodeEdit>,
) -> Result<bool, GraphError> {
    let project = tx.structure_root(STRUCTURE_PROJECT)?;
    match tx
        .commit_edits(
            Commit {
                namespace: namespace.into(),
                title: title.into(),
                intent: title.into(),
                by: "graphsvc.seed".into(),
                baseline_name: title.into(),
                edits,
                project_id: project,
                ..Commit::default()
            },
            &|_: &str, _: &str, _: &Transition| Ok(()),
        )
        .await
    {
        Err(e) if e.message.ends_with(&format!("{NOTHING_TO_COMMIT}: invalid")) => Ok(false),
        r => r.map(|_| true),
    }
}

/// Whether a namespace holds a node of a key (`Graph.NodeByKey` finding one).
async fn has_key(tx: &Tx<'_, Session>, ns: &str, key: &str) -> Result<bool, GraphError> {
    Ok(tx.store.node_id_by_key(ns, key).await?.is_some())
}

/// The nodes of a namespace at the head of main, by key.
async fn head_nodes(tx: &Tx<'_, Session>, ns: &str, typ: &str) -> Result<HashMap<String, Node>, GraphError> {
    let w = World::open(tx.store, &View::branch(ns, MAIN_BRANCH)).await?;
    let q = NodeQuery {
        typ: typ.into(),
        limit: i32::MAX as usize,
        ..NodeQuery::default()
    };
    Ok(w.page(&q)
        .await?
        .nodes
        .into_iter()
        .filter(|n| !n.deleted && (typ.is_empty() || n.typ == typ))
        .map(|n| (n.key.clone(), n))
        .collect())
}

/// Whether the properties of a node hold the wanted ones, compared as Go's JSON (`sameProps`).
fn same_props(have: &Option<Map<String, Json>>, want: &Map<String, Json>) -> bool {
    let have = have.clone().map(Json::Object).unwrap_or(Json::Null);
    gojson::to_string(&have) == gojson::to_string(want)
}

/// The default policies as Policy nodes (`SeedAccess`), unless the floor policy exists: edited or deleted policies stay so.
async fn seed_access(tx: &mut Tx<'_, Session>, s: &Seeds) -> Result<bool, GraphError> {
    if has_key(tx, graph_core::model::NAMESPACE_ORGANISATION, &s.floor_policy).await? {
        return Ok(false);
    }
    let edits = s.policies.iter().map(SeedNode::edit).collect();
    seed_change(tx, graph_core::model::NAMESPACE_ORGANISATION, "Default policies", edits).await?;
    Ok(true)
}

/// The built-in MCPs, their adapter definitions and the platform roles as the code states them (`SeedBuiltins`):
/// whether it wrote, and the built-in MCPs seeded for the first time.
async fn seed_builtins(tx: &mut Tx<'_, Session>, s: &Seeds) -> Result<(bool, Vec<String>), GraphError> {
    let current = head_nodes(tx, PLATFORM, "").await?;
    let fresh: Vec<String> = s
        .builtin_mcps
        .iter()
        .filter(|name| !current.contains_key(&format!("MCP:{name}")))
        .cloned()
        .collect();
    let mut edits = Vec::new();
    for b in &s.builtins {
        match current.get(&b.key) {
            None => edits.push(b.edit()),
            Some(n) if !same_props(&n.props, &b.props) => {
                let mut set = b.props.clone();
                for k in n.props.iter().flat_map(|p| p.keys()) {
                    if !b.props.contains_key(k) {
                        // properties are merged: clear the ones the code dropped
                        set.insert(k.clone(), Json::Null);
                    }
                }
                edits.push(NodeEdit {
                    pre: Some(n.node_ref()),
                    props: Some(set),
                    rationale: format!("Built-in {} follows the platform", b.key),
                    ..NodeEdit::default()
                });
            }
            Some(_) => {}
        }
    }
    let wrote = !edits.is_empty();
    if wrote {
        seed_change(tx, PLATFORM, "Built-in MCPs", edits).await?;
    }
    Ok((wrote, fresh))
}

/// The instance of its adapter the root unit gets for each built-in MCP seeded for the first time, never recreated
/// afterwards (the second commit of `SeedBuiltins`).
async fn seed_builtin_adapters(tx: &mut Tx<'_, Session>, s: &Seeds, fresh: &[String]) -> Result<(), GraphError> {
    let root = tx.structure_root(STRUCTURE_ORGANISATION)?;
    let mut instances = Vec::new();
    for name in fresh {
        let Some(a) = s.adapters.get(name) else { continue };
        let key = format!("ADP:{root}/{name}");
        if has_key(tx, graph_core::model::NAMESPACE_ORGANISATION, &key).await? {
            continue;
        }
        instances.push(
            SeedNode {
                key,
                owner: root.clone(),
                ..a.clone()
            }
            .edit(),
        );
    }
    if !instances.is_empty() {
        seed_change(
            tx,
            graph_core::model::NAMESPACE_ORGANISATION,
            "Built-in adapters of the default organisation",
            instances,
        )
        .await?;
    }
    Ok(())
}

/// The model gateway configuration, when the graph holds no provider yet (`SeedModels`).
async fn seed_models(tx: &mut Tx<'_, Session>, nodes: &[SeedNode]) -> Result<bool, GraphError> {
    if !head_nodes(tx, PLATFORM, "platform@LlmProvider").await?.is_empty() || nodes.is_empty() {
        return Ok(false);
    }
    let edits = nodes.iter().map(SeedNode::edit).collect();
    seed_change(tx, PLATFORM, "Model gateway configuration", edits).await?;
    Ok(true)
}

/// An alias as `llmcfg.AliasFromProps` reads it: its target and whether it is protected, none when it does not read.
fn alias_of(props: &Option<Map<String, Json>>) -> Option<(String, bool)> {
    let p = props.as_ref()?;
    let text = |k: &str| match p.get(k) {
        None | Some(Json::Null) => Some(String::new()),
        Some(Json::String(s)) => Some(s.clone()),
        Some(_) => None,
    };
    let alias = text("alias")?;
    let target = text("target")?;
    let protected = match p.get("protected") {
        None | Some(Json::Null) => false,
        Some(Json::Bool(b)) => *b,
        Some(_) => return None,
    };
    let valid = alias.len() <= 40
        && alias
            .chars()
            .enumerate()
            .all(|(i, c)| c.is_ascii_lowercase() || c.is_ascii_digit() || (i > 0 && (c == '_' || c == '-')))
        && !alias.is_empty();
    valid.then_some((target, protected))
}

/// The protected aliases on every start (`SeedProtectedAliases`, ADR 0084): a missing one targets what the default
/// alias targets (the helper: the fast one when it exists); one not flagged or retired is flagged and restored; one that
/// exists is never retargeted.
async fn seed_protected_aliases(tx: &mut Tx<'_, Session>, s: &Seeds) -> Result<bool, GraphError> {
    let current = head_nodes(tx, PLATFORM, "platform@LlmAlias").await?;
    let target_of = |alias: &str| -> String {
        match current.get(&format!("LLA:{alias}")) {
            Some(n) if n.state != "retired" => alias_of(&n.props).map(|(t, _)| t).unwrap_or_default(),
            _ => String::new(),
        }
    };
    let mut edits = Vec::new();
    for name in &s.protected_aliases {
        let key = format!("LLA:{name}");
        let Some(n) = current.get(&key) else {
            let mut target = target_of("default");
            if name == "helper" {
                let fast = target_of("fast");
                if !fast.is_empty() {
                    target = fast;
                }
            }
            let props = serde_json::json!({"alias": name, "target": target, "protected": true});
            edits.push(
                SeedNode {
                    key,
                    typ: "platform@LlmAlias".into(),
                    props: props.as_object().cloned().unwrap_or_default(),
                    owner: String::new(),
                }
                .edit(),
            );
            continue;
        };
        if alias_of(&n.props).is_some_and(|(_, p)| p) && n.state != "retired" {
            continue;
        }
        let mut props = Map::new();
        props.insert("protected".into(), true.into());
        edits.push(NodeEdit {
            pre: Some(n.node_ref()),
            props: Some(props),
            rationale: format!("Alias {name} is protected"),
            state: if n.state == "retired" {
                "active".into()
            } else {
                String::new()
            },
            ..NodeEdit::default()
        });
    }
    if edits.is_empty() {
        return Ok(false);
    }
    seed_change(tx, PLATFORM, "Protected model aliases", edits).await?;
    Ok(true)
}

/// The built-in LLM behaviour, created once and never touched afterwards (`SeedBehaviors`, ADR 0093).
async fn seed_behaviors(tx: &mut Tx<'_, Session>, s: &Seeds) -> Result<bool, GraphError> {
    if has_key(tx, PLATFORM, &s.behavior.key).await? {
        return Ok(false);
    }
    seed_change(tx, PLATFORM, "Built-in LLM behaviours", vec![s.behavior.edit()]).await?;
    Ok(true)
}

/// The properties of the node declaring a source (`graphsvc.SourceProps`).
fn source_props(s: &Source) -> Map<String, Json> {
    let mut p = Map::new();
    p.insert("name".into(), s.name.as_str().into());
    p.insert("kind".into(), s.kind.as_str().into());
    for (k, v) in [
        ("namespace", &s.namespace),
        ("location", &s.location),
        ("branch", &s.branch),
    ] {
        if !v.is_empty() {
            p.insert(k.into(), v.as_str().into());
        }
    }
    if !s.namespaces.is_empty() {
        p.insert("namespaces".into(), s.namespaces.clone().into());
    }
    p
}

/// The platform graph's own source and the sources given that the graph does not declare yet (`SeedSources`).
async fn seed_sources(tx: &mut Tx<'_, Session>, s: &Seeds, extra: &[Source]) -> Result<bool, GraphError> {
    let mut wanted = vec![s.platform_source.clone()];
    for src in extra {
        src.validate().map_err(GraphError::invalid)?;
        wanted.push(SeedNode {
            key: format!("SRC:{}", src.name),
            typ: s.platform_source.typ.clone(),
            props: source_props(src),
            owner: String::new(),
        });
    }
    let mut edits = Vec::new();
    for n in &wanted {
        if !has_key(tx, PLATFORM, &n.key).await? {
            edits.push(n.edit());
        }
    }
    if edits.is_empty() {
        return Ok(false);
    }
    seed_change(tx, PLATFORM, "Sources of the platform", edits).await?;
    Ok(true)
}

/// The document-repository MCP and the definition of its localfs adapter, once (`devseed.DocumentRepository`).
async fn seed_document_repository(tx: &mut Tx<'_, Session>, s: &Seeds) -> Result<bool, GraphError> {
    let Some(mcp) = s.document_repository.first() else {
        return Ok(false);
    };
    if has_key(tx, PLATFORM, &mcp.key).await? {
        return Ok(false);
    }
    let edits = s.document_repository.iter().map(SeedNode::edit).collect();
    seed_change(tx, PLATFORM, "MCP document-repository", edits).await?;
    Ok(true)
}

/// The graph sources a bootstrap declares (`graphsvc.ParseSources`, `GOAP_GRAPH_SOURCES`):
/// `name=ns1,ns2@url;name2=ns3@url2`.
pub fn parse_sources(spec: &str) -> Result<Vec<Source>, String> {
    let mut out = Vec::new();
    for group in spec.split(';').map(str::trim).filter(|g| !g.is_empty()) {
        let parsed = group
            .split_once('=')
            .and_then(|(name, rest)| rest.split_once('@').map(|(names, base)| (name, names, base)))
            .filter(|(_, _, base)| !base.trim().is_empty());
        let Some((name, names, base)) = parsed else {
            return Err(format!(
                "graph sources: want name=namespaces@url, got {}",
                goyaml_quote(group)
            ));
        };
        let s = Source {
            name: name.trim().into(),
            kind: graph_core::sources::KIND_GRAPH.into(),
            location: base.trim().into(),
            namespaces: names
                .split(',')
                .map(str::trim)
                .filter(|n| !n.is_empty())
                .map(String::from)
                .collect(),
            ..Source::default()
        };
        if s.namespaces.is_empty() {
            return Err(format!("graph sources: {} holds no namespace", s.name));
        }
        s.validate()?;
        out.push(s);
    }
    Ok(out)
}

/// The platform namespace.
const PLATFORM: &str = "platform";

impl Graph {
    /// One step of the boot: its own transaction, as the graph service.
    async fn boot_step<T, F>(&self, what: &str, f: F) -> anyhow::Result<T>
    where
        F: for<'a, 'b> AsyncFnOnce(&'a mut Tx<'b, Session>) -> Result<T, GraphError>,
    {
        self.transact(&system(), String::new(), f)
            .await
            .map(Response::into_inner)
            .map_err(|e| anyhow::anyhow!("{what}: {}", e.message()))
    }

    /// The platform bootstrap (Go's `graphsvc.Boot`): the roots of the structures, the default policies, the built-in
    /// MCPs, the models, the protected aliases, the built-in behaviour, the sources, then the dev seed if any. Every
    /// step is idempotent, so a second call writes nothing.
    pub async fn boot(&self, o: &BootOptions) -> anyhow::Result<Report> {
        let s = seeds();
        // a configuration that does not make nodes seeds none, as Go's `BootConfig` failing leaves no model
        let models = o.models.nodes().unwrap_or_else(|e| {
            tracing::error!(err = %e, "models config");
            Vec::new()
        });
        let mut r = Report {
            roots: self.boot_step("bootstrap", async |tx| tx.bootstrap().await).await?,
            ..Report::default()
        };
        r.access = self
            .boot_step("seed access", async |tx| seed_access(tx, &s).await)
            .await?;
        let (wrote, fresh) = self
            .boot_step("seed built-in MCPs", async |tx| seed_builtins(tx, &s).await)
            .await?;
        r.builtins = wrote || !fresh.is_empty();
        if !fresh.is_empty() {
            self.boot_step("seed built-in MCPs", async |tx| {
                seed_builtin_adapters(tx, &s, &fresh).await
            })
            .await?;
        }
        r.models = self
            .boot_step("seed models", async |tx| seed_models(tx, &models).await)
            .await?;
        r.aliases = self
            .boot_step("seed protected aliases", async |tx| {
                seed_protected_aliases(tx, &s).await
            })
            .await?;
        r.behaviors = self
            .boot_step("seed LLM behaviors", async |tx| seed_behaviors(tx, &s).await)
            .await?;
        r.sources = self
            .boot_step("seed sources", async |tx| seed_sources(tx, &s, &o.sources).await)
            .await?;
        if o.dev == DevSeed::Demo {
            r.dev = self.load_demo().await.map_err(|e| anyhow::anyhow!("dev seed: {e}"))?;
        }
        if o.dev != DevSeed::None {
            r.dev |= self
                .boot_step("dev seed", async |tx| seed_document_repository(tx, &s).await)
                .await?;
        }
        Ok(r)
    }
}

/// A secret (`platform.Secrets.Get`): the field of a KV v2 path of Vault (`VAULT_ADDR`, `VAULT_TOKEN`, mount
/// `VAULT_KV_MOUNT`, default `secret`) given as `<path>#<field>`, else the environment variable.
async fn secret(reference: &str, env: &str) -> String {
    let var = |k: &str| std::env::var(k).unwrap_or_default();
    let addr = var("VAULT_ADDR");
    if let (false, Some((path, field))) = (addr.is_empty(), reference.split_once('#')) {
        let mount = Some(var("VAULT_KV_MOUNT"))
            .filter(|m| !m.is_empty())
            .unwrap_or_else(|| "secret".into());
        let url = format!("{}/v1/{mount}/data/{path}", addr.trim_end_matches('/'));
        let got = async {
            let r = reqwest::Client::builder()
                .timeout(std::time::Duration::from_secs(5))
                .build()?
                .get(&url)
                .header("X-Vault-Token", var("VAULT_TOKEN"))
                .send()
                .await?
                .error_for_status()?;
            let body: Json = r.json().await?;
            anyhow::Ok(body["data"]["data"][field].as_str().unwrap_or_default().to_string())
        }
        .await;
        if let Ok(v) = got
            && !v.is_empty()
        {
            return v;
        }
    }
    var(env)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_models_config_seeds_what_go_seeds() {
        for c in seeds().models {
            let got: Vec<_> = c
                .config
                .nodes()
                .unwrap()
                .into_iter()
                .map(|n| (n.key, n.typ, gojson::to_string(&n.props)))
                .collect();
            let want: Vec<_> = c
                .nodes
                .into_iter()
                .map(|n| (n.key, n.typ, gojson::to_string(&n.props)))
                .collect();
            assert_eq!(got, want);
        }
        let d = ModelsConfig::default_for(false).nodes().unwrap();
        assert_eq!(d.len(), seeds().models[1].nodes.len());
    }

    #[test]
    fn sources_read_as_go_reads_them() {
        let s = parse_sources(" content=alm, code @http://graph-content:8080 ;x=y@u").unwrap();
        assert_eq!(s.len(), 2);
        assert_eq!(s[0].namespaces, vec!["alm", "code"]);
        assert_eq!(s[0].location, "http://graph-content:8080");
        assert_eq!(
            parse_sources("nope").unwrap_err(),
            r#"graph sources: want name=namespaces@url, got "nope""#
        );
        assert_eq!(
            parse_sources("a=@u").unwrap_err(),
            "graph sources: a holds no namespace"
        );
    }
}
