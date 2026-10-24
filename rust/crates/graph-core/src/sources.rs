//! The sources logical URLs name (ADR 0154): declared by the `platform@Source` nodes in force at the head of main of
//! the platform namespace, mapped per project (§8), as Go's `Graph.Lnk().Source` and `Graph.mappingOf` read them.
use std::collections::HashMap;

use serde_json::Value;

use crate::{
    Result,
    access::STATE_RETIRED,
    codetext::go_quote,
    model::{MAIN_BRANCH, NAMESPACE_PLATFORM, Node, NodeRef},
    read,
    store::ReadTx,
};

/// The type of a source's declaration (`domain.TypeSource`) and the prefix of its key.
const TYPE_SOURCE: &str = "platform@Source";
const KEY_PREFIX: &str = "SRC:";

/// The kind of a source that is a graph, and of one of files (`lnk.KindGraph`, `lnk.KindFiles`).
pub const KIND_GRAPH: &str = "graph";
pub const KIND_FILES: &str = "files";

/// A source of logical URLs (`lnk.Source`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Source {
    pub name: String,
    pub kind: String,
    pub namespace: String,
    pub namespaces: Vec<String>,
    pub location: String,
    pub branch: String,
}

impl Source {
    /// The namespace the source reads: its namespace, else its name.
    pub fn namespace_of(&self) -> &str {
        if self.namespace.is_empty() {
            &self.name
        } else {
            &self.namespace
        }
    }

    /// Whether the source is a graph at a location holding a namespace (the content graph, ADR 0154 §9).
    pub fn holds(&self, ns: &str) -> bool {
        self.kind == KIND_GRAPH && !self.location.is_empty() && self.namespaces.iter().any(|n| n == ns)
    }

    /// Whether this graph holds the source: a graph source with no location.
    pub fn local(&self) -> bool {
        self.kind == KIND_GRAPH && self.location.is_empty()
    }

    /// The checks of a definition (`lnk.Source.Validate`), in Go's words.
    fn validate(&self) -> std::result::Result<(), String> {
        if !source_name(&self.name) {
            return Err(format!(
                "source {}: want [a-z][a-z0-9_.-]*: invalid lnk",
                go_quote(&self.name)
            ));
        }
        if self.kind.is_empty() {
            return Err(format!("source {}: no kind: invalid lnk", go_quote(&self.name)));
        }
        if self.kind == KIND_FILES && self.location.is_empty() {
            return Err(format!(
                "source {} of kind {}: no location: invalid lnk",
                go_quote(&self.name),
                self.kind
            ));
        }
        Ok(())
    }
}

/// `^[a-z][a-z0-9_.-]{0,62}$` (`lnk.sourceRE`).
fn source_name(s: &str) -> bool {
    let mut cs = s.chars();
    s.len() <= 63
        && cs.next().is_some_and(|c| c.is_ascii_lowercase())
        && cs.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, '_' | '.' | '-'))
}

/// The source a declaration states (`domain.SourceOf`).
fn source_of(n: &Node) -> std::result::Result<Source, String> {
    let str = |k: &str| {
        n.props
            .as_ref()
            .and_then(|p| p.get(k))
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    let Some(name) = n.key.strip_prefix(KEY_PREFIX) else {
        return Err(format!(
            "source node {}: want key {KEY_PREFIX}<name>: invalid lnk",
            go_quote(&n.key)
        ));
    };
    let p = str("name");
    if !p.is_empty() && p != name {
        return Err(format!(
            "source node {} names {}: invalid lnk",
            go_quote(&n.key),
            go_quote(&p)
        ));
    }
    let src = Source {
        name: name.to_string(),
        kind: str("kind"),
        namespace: str("namespace"),
        namespaces: n
            .props
            .as_ref()
            .and_then(|p| p.get("namespaces"))
            .and_then(Value::as_array)
            .map(|l| {
                l.iter()
                    .filter_map(Value::as_str)
                    .filter(|s| !s.is_empty())
                    .map(str::to_string)
                    .collect()
            })
            .unwrap_or_default(),
        location: str("location"),
        branch: str("branch"),
    };
    src.validate().map(|_| src)
}

/// The source of a name (`lnk.Registry.Source` over Go's `declaredSource`): the declaration of that name in force on
/// main (a malformed one is an error), else the namespace of that name a graph source holds, else the namespace of that
/// name of this graph. The error is Go's words, before its `: invalid`.
pub async fn source<T: ReadTx>(tx: &T, name: &str) -> Result<std::result::Result<Source, String>> {
    if !source_name(name) {
        return Ok(Err(format!("source {}: invalid lnk", go_quote(name))));
    }
    let head = read::branch_head(tx, NAMESPACE_PLATFORM, MAIN_BRANCH).await?;
    let at = read::select(tx, &head, None).await?;
    let refs: Vec<NodeRef> = at.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect();
    let mut all = Vec::new();
    let mut broken = HashMap::new();
    for n in tx.nodes_at(&refs, TYPE_SOURCE).await? {
        if n.deleted || n.state == STATE_RETIRED {
            continue;
        }
        match source_of(&n) {
            Ok(src) => all.push(src),
            Err(e) => {
                broken.insert(n.key.strip_prefix(KEY_PREFIX).unwrap_or(&n.key).to_string(), e);
            }
        }
    }
    if let Some(e) = broken.remove(name) {
        return Ok(Err(e));
    }
    all.sort_by(|a, b| a.name.cmp(&b.name));
    if let Some(src) = all.iter().find(|s| s.name == name) {
        return Ok(Ok(src.clone()));
    }
    if let Some(src) = all.iter().find(|s| s.holds(name)) {
        return Ok(Ok(Source {
            name: name.to_string(),
            kind: KIND_GRAPH.into(),
            namespace: name.to_string(),
            namespaces: Vec::new(),
            location: src.location.clone(),
            branch: src.branch.clone(),
        }));
    }
    Ok(Ok(Source {
        name: name.to_string(),
        kind: KIND_GRAPH.into(),
        ..Source::default()
    }))
}
