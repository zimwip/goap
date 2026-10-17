use std::collections::BTreeMap;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{self as domain, BaselineEvent, ChangeDocEvent, NodeEvent, NodeID};
use serde_json::{Map, Value};
use sha2::{Digest, Sha256};

use crate::{Doc, Embedder, Error, KIND_CHANGE, KIND_NODE, Store};

/// Told when a document could not be embedded: its key and the error.
pub type EmbedFailed = Arc<dyn Fn(&str, &Error) + Send + Sync>;

/// Indexer keeps a store in step with the events of the graph.
#[derive(Clone)]
pub struct Indexer {
    pub store: Arc<dyn Store>,
    /// None: text-only index.
    pub embedder: Option<Arc<dyn Embedder>>,
    /// EmbedFailed is told when a document could not be embedded. It is then indexed as text only, with an empty hash
    /// so that the next delivery or a reindex embeds it again.
    pub embed_failed: Option<EmbedFailed>,
}

/// Bounds of the text of a change document (ADR 0095): the title, the intent and the goal are short by nature, and a
/// change acting on many nodes gives their keys up to the cap. The items, decisions, reviews and drafts are not
/// indexed.
pub const MAX_CHANGE_FIELD: usize = 2000;
pub const MAX_CHANGE_TEXT: usize = 4000;

impl Indexer {
    /// An indexer of a store, text only.
    pub fn new(store: Arc<dyn Store>) -> Self {
        Indexer {
            store,
            embedder: None,
            embed_failed: None,
        }
    }

    /// OnNode indexes a written node version. Deleted versions are indexed too (flagged), so they can be excluded by
    /// the main facet and stay searchable in the history of a branch.
    pub async fn on_node(&self, ctx: &Ctx, ev: &NodeEvent) -> Result<(), Error> {
        self.put(
            ctx,
            Doc {
                kind: KIND_NODE.to_string(),
                id: ev.id.clone(),
                version: ev.version,
                namespace: ev.namespace.clone(),
                r#type: ev.r#type.clone(),
                key: ev.key.clone(),
                state: ev.state.clone(),
                branch: ev.branch.clone(),
                deleted: ev.deleted,
                project: ev.project.clone(),
                owner: ev.owner.clone(),
                text: document_text(&ev.key, &ev.text),
                time: ev.time,
                facets: facet_strings(&ev.facets),
                ..Default::default()
            },
        )
        .await
    }

    /// OnChange indexes the document of a change, or drops it when the change was purged. personal_to is the subject
    /// a personal change belongs to, decided by the caller (the index knows no organisation).
    pub async fn on_change(
        &self,
        ctx: &Ctx,
        ev: &ChangeDocEvent,
        personal_to: &str,
    ) -> Result<(), Error> {
        if ev.deleted {
            return self
                .store
                .delete(ctx, KIND_CHANGE, &NodeID::from(ev.id.to_string()))
                .await;
        }
        let branch = if ev.branch.is_empty() {
            domain::MAIN_BRANCH.to_string()
        } else {
            ev.branch.clone()
        };
        self.put(
            ctx,
            Doc {
                kind: KIND_CHANGE.to_string(),
                id: NodeID::from(ev.id.to_string()),
                namespace: ev.namespace.clone(),
                key: ev.id.to_string(),
                state: ev.state.clone(),
                branch,
                main: true,
                project: ev.project_id.clone(),
                owner: ev.owner_org.clone(),
                status: ev.status.to_string(),
                methodology: ev.methodology.clone(),
                parent: ev.parent_id.to_string(),
                personal_to: personal_to.to_string(),
                title: ev.title.clone(),
                text: change_text(ev),
                time: ev.created_at,
                ..Default::default()
            },
        )
        .await
    }

    /// put embeds the document unless its text is unchanged and already embedded, then stores it.
    async fn put(&self, ctx: &Ctx, mut d: Doc) -> Result<(), Error> {
        d.hash = hex(&Sha256::digest(d.text.as_bytes()));
        let stored = self.store.hash(ctx, &d.kind, &d.id, d.version).await?;
        if let Some(emb) = &self.embedder {
            let same = matches!(&stored, Some((hash, true)) if *hash == d.hash);
            if !same {
                match emb.embed(ctx, std::slice::from_ref(&d.text)).await {
                    Err(e) => {
                        d.hash = String::new();
                        if let Some(f) = &self.embed_failed {
                            f(&d.key, &e);
                        }
                    }
                    Ok(mut vecs) if vecs.len() == 1 => d.embedding = vecs.pop(),
                    Ok(_) => {}
                }
            }
        }
        self.store.upsert(ctx, d).await
    }

    /// OnBaseline follows the head of main: only its diffs matter to the main facet.
    pub async fn on_baseline(&self, ctx: &Ctx, ev: &BaselineEvent) -> Result<(), Error> {
        if domain::branch_of(&ev.branch) != domain::MAIN_BRANCH {
            return Ok(());
        }
        self.store.set_main(ctx, &ev.set, &ev.removed).await
    }
}

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// ChangeText builds the text of a change: its title, intent and goal, its methodology and namespace, then the keys
/// (and types) of the nodes it acts on, bounded.
pub fn change_text(ev: &ChangeDocEvent) -> String {
    let mut b = ev.id.to_string();
    let mut add = |name: &str, v: &str| {
        if !v.is_empty() {
            b.push_str(&format!("\n{name}: {}", truncate(v, MAX_CHANGE_FIELD)));
        }
    };
    add("title", &ev.title);
    add("intent", &ev.intent);
    add("goal", &ev.goal);
    add("methodology", &ev.methodology);
    add("namespace", &ev.namespace);
    if !ev.impacts.is_empty() {
        let parts: Vec<String> = ev
            .impacts
            .iter()
            .map(|r| {
                if r.r#type.is_empty() {
                    r.key.clone()
                } else {
                    format!("{} ({})", r.key, r.r#type)
                }
            })
            .collect();
        b.push_str(&format!("\nnodes: {}", parts.join(", ")));
    }
    truncate(&b, MAX_CHANGE_TEXT)
}

fn truncate(s: &str, max: usize) -> String {
    match s.char_indices().nth(max) {
        Some((i, _)) => s[..i].to_string(),
        None => s.to_string(),
    }
}

/// DocumentText builds the text of a node: its key then its searchable properties, in a stable order.
pub fn document_text(key: &str, text: &BTreeMap<String, String>) -> String {
    let mut b = key.to_string();
    for (k, v) in text {
        b.push_str(&format!("\n{k}: {v}"));
    }
    b
}

/// fmt.Sprint of a JSON value of a facet.
fn sprint(v: &Value) -> String {
    match v {
        Value::Null => "<nil>".to_string(),
        Value::Bool(b) => b.to_string(),
        Value::String(s) => s.clone(),
        Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                i.to_string()
            } else if let Some(u) = n.as_u64() {
                u.to_string()
            } else {
                let f = n.as_f64().unwrap_or_default();
                let abs = f.abs();
                if abs != 0.0 && !(1e-4..1e21).contains(&abs) {
                    let s = format!("{f:e}");
                    let (mant, exp) = s.split_once('e').unwrap_or((&s, "0"));
                    let (sign, digits) = match exp.strip_prefix('-') {
                        Some(d) => ('-', d),
                        None => ('+', exp),
                    };
                    format!("{mant}e{sign}{digits:0>2}")
                } else {
                    format!("{f}")
                }
            }
        }
        Value::Array(a) => format!("[{}]", a.iter().map(sprint).collect::<Vec<_>>().join(" ")),
        Value::Object(m) => format!(
            "map[{}]",
            m.iter()
                .map(|(k, v)| format!("{k}:{}", sprint(v)))
                .collect::<Vec<_>>()
                .join(" ")
        ),
    }
}

fn facet_strings(m: &Map<String, Value>) -> BTreeMap<String, String> {
    m.iter().map(|(k, v)| (k.clone(), sprint(v))).collect()
}
