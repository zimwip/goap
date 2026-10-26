//! The methodologies the registry declares, as the graph reads them to instantiate what their changes follow (Go's
//! `registrysvc.Service` held by `platform/cmd/graph` over the platform graph): the version a change follows, the goal
//! it starts with, the lifecycle it moves along and its guardian chain (ADR 0058, 0096, 0106, 0113). The registry keeps
//! each version as a `MethodologyVersion` header and one node per element in the `methodology` namespace (ADR 0023);
//! what is read here is the header, its goals and processes and the link naming its main goal, at the head of main.
use std::collections::HashMap;

use chrono::{DateTime, Utc};
use serde::Deserialize;
use serde_json::Value;

use graph_catalog::{Catalog, def::Lifecycle};
use std::sync::Arc;

use crate::{
    GraphError, Result,
    change::GuardianRef,
    model::{Link, MAIN_BRANCH, NAMESPACE_METHODOLOGY, Node, NodeId, NodeRef},
    read,
    store::ReadTx,
};

pub const TYPE_VERSION: &str = "methodology@MethodologyVersion";
pub const TYPE_GOAL: &str = "methodology@Goal";
pub const TYPE_PROCESS: &str = "methodology@Process";
/// The link of a version to its main goal, a goal or a process (`methodology.LinkMainGoal`).
pub const LINK_MAIN_GOAL: &str = "methodology@main_goal";
pub const STATUS_PUBLISHED: &str = "published";
pub const STATUS_DRAFT: &str = "draft";
const STATUS_DELETED: &str = "deleted";
/// An integration naming every other methodology (`methodology.AnyMethodology`).
const ANY_METHODOLOGY: &str = "*";
/// The longest guardian chain (`registrysvc`'s `maxChain`).
const MAX_CHAIN: usize = 8;

/// The node types a declaration is read from.
pub const TYPES: [&str; 3] = [TYPE_VERSION, TYPE_GOAL, TYPE_PROCESS];

/// A methodology reference split into its name and version (`domain.SplitMethodologyRef`): a bare name has none.
pub fn split_ref(reference: &str) -> (&str, &str) {
    match reference.rfind('@') {
        Some(i) if i > 0 => (&reference[..i], &reference[i + 1..]),
        _ => (reference, ""),
    }
}

/// A reference of a methodology at a version (`domain.MethodologyRef`).
pub fn reference(name: &str, version: &str) -> String {
    if version.is_empty() {
        name.to_string()
    } else {
        format!("{name}@{version}")
    }
}

/// How a methodology integrates the changes of others (`methodology.Integration`).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Integration {
    pub methodologies: Vec<String>,
    pub exclusive: bool,
    pub attach: String,
}

/// A version of a methodology as the registry stores it.
#[derive(Debug, Clone, Default)]
pub struct Declaration {
    pub name: String,
    pub version: String,
    pub status: String,
    pub published_at: Option<DateTime<Utc>>,
    /// the namespace its changes act on
    pub namespace: String,
    /// the lifecycle its changes follow, by name
    pub lifecycle: String,
    /// its main goal (`Methodology.MainGoal`): declared, else its first goal, else its first process
    pub goal: String,
    pub integrates: Vec<Integration>,
}

impl Declaration {
    /// The integration naming another methodology (`Methodology.IntegrationOf`).
    pub fn integration_of(&self, other: &str) -> Option<&Integration> {
        if other == self.name {
            return None;
        }
        self.integrates
            .iter()
            .find(|i| i.methodologies.iter().any(|m| m == other || m == ANY_METHODOLOGY))
    }

    /// Whether the changes of this methodology integrate the changes of another (`Methodology.IntegratesChangesOf`).
    pub fn integrates_changes_of(&self, other: &str) -> bool {
        self.integration_of(other).is_some()
    }
}

/// The versions the registry declares.
#[derive(Debug, Clone, Default)]
pub struct Declared {
    versions: Vec<Declaration>,
}

fn text(n: &Node, k: &str) -> String {
    n.props
        .as_ref()
        .and_then(|p| p.get(k))
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

fn removed(n: &Node) -> bool {
    n.props.as_ref().and_then(|p| p.get("removed")).and_then(Value::as_bool) == Some(true)
}

fn position(n: &Node) -> f64 {
    n.props
        .as_ref()
        .and_then(|p| p.get("position"))
        .and_then(Value::as_f64)
        .unwrap_or_default()
}

impl Declared {
    /// The declarations of the nodes of the namespace of the methodologies and the links leaving them (Go's
    /// `buildDefs`, what a change follows only).
    pub fn of(nodes: &[Node], links: &[Link]) -> Declared {
        let by_id: HashMap<&NodeId, &Node> = nodes.iter().filter(|n| !n.deleted).map(|n| (&n.id, n)).collect();
        // the elements of a header: the nodes whose key starts with it, `<header>/<kind>/<name>`
        let mut elements: HashMap<&str, Vec<&Node>> = HashMap::new();
        for n in by_id.values() {
            if (n.typ == TYPE_GOAL || n.typ == TYPE_PROCESS) && !removed(n) {
                let parent = n.key.split_once('/').map_or(n.key.as_str(), |(p, _)| p);
                elements.entry(parent).or_default().push(n);
            }
        }
        let mut versions = Vec::new();
        for n in nodes.iter().filter(|n| !n.deleted && n.typ == TYPE_VERSION) {
            let first = |typ: &str| {
                let mut of: Vec<&&Node> = elements
                    .get(n.key.as_str())
                    .map(|l| l.iter().filter(|e| e.typ == typ).collect())
                    .unwrap_or_default();
                of.sort_by(|a, b| a.key.cmp(&b.key));
                of.sort_by(|a, b| position(a).total_cmp(&position(b)));
                of.first().map(|e| text(e, "name"))
            };
            // the goal it names: the element its link reaches, else the name its property kept (unresolved)
            let linked = links
                .iter()
                .filter(|l| l.typ == LINK_MAIN_GOAL && l.from.id == n.id && l.from.version == n.version)
                .filter_map(|l| by_id.get(&l.to_node()?.id))
                .find(|t| t.typ == TYPE_GOAL || t.typ == TYPE_PROCESS)
                .map(|t| text(t, "name"));
            let goal = linked
                .filter(|g| !g.is_empty())
                .or_else(|| Some(text(n, "goal")).filter(|g| !g.is_empty()))
                .or_else(|| first(TYPE_GOAL))
                .or_else(|| first(TYPE_PROCESS))
                .unwrap_or_default();
            let integrates = n
                .props
                .as_ref()
                .and_then(|p| p.get("integrates"))
                .and_then(|v| serde_json::from_value(v.clone()).ok())
                .unwrap_or_default();
            versions.push(Declaration {
                name: text(n, "name"),
                version: text(n, "version"),
                status: text(n, "status"),
                published_at: DateTime::parse_from_rfc3339(&text(n, "publishedAt"))
                    .ok()
                    .map(|t| t.with_timezone(&Utc)),
                namespace: text(n, "namespace"),
                lifecycle: text(n, "lifecycle"),
                goal,
                integrates,
            });
        }
        Declared { versions }
    }

    /// The declarations at the head of main of the namespace of the methodologies, or of the platform graph when this
    /// graph is a content graph (ADR 0154 §9).
    pub async fn read<T: ReadTx>(tx: &T) -> Result<Declared> {
        if let Some(p) = tx.platform() {
            return Ok(Declared::of(&p.methodology.nodes, &p.methodology.links));
        }
        let head = read::branch_head(tx, NAMESPACE_METHODOLOGY, MAIN_BRANCH).await?;
        let at = read::select(tx, &head, None).await?;
        let refs: Vec<NodeRef> = at.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect();
        let mut nodes = Vec::new();
        for typ in TYPES {
            nodes.extend(tx.nodes_at(&refs, typ).await?);
        }
        let headers: Vec<NodeRef> = nodes
            .iter()
            .filter(|n| n.typ == TYPE_VERSION)
            .map(|n| NodeRef::new(n.id.clone(), n.version))
            .collect();
        let links = tx.links_from(&headers, Some(&[LINK_MAIN_GOAL.to_string()])).await?;
        Ok(Declared::of(&nodes, &links))
    }

    /// The version a reference names, not deleted, else for a bare name its latest published version (Go's
    /// `GraphStore.Get`).
    pub fn get(&self, reference: &str) -> Option<&Declaration> {
        let (name, version) = split_ref(reference);
        let mut of = self.versions.iter().filter(|d| d.name == name);
        if !version.is_empty() {
            return of.find(|d| d.version == version && d.status != STATUS_DELETED);
        }
        of.filter(|d| d.status == STATUS_PUBLISHED)
            .fold(None, |best: Option<&Declaration>, d| match best {
                Some(b) if d.published_at <= b.published_at => Some(b),
                _ => Some(d),
            })
    }

    /// The version a new change of a methodology follows (`registrysvc.Service.MethodologyVersion`, ADR 0113): the one a
    /// reference names when it is published or archived, the latest published one for a bare name; none when the
    /// registry does not know the methodology.
    pub fn version_of(&self, reference: &str) -> Result<String> {
        let (name, version) = split_ref(reference);
        match self.get(reference) {
            None if version.is_empty() => Ok(String::new()),
            None => Err(GraphError::invalid(format!(
                "methodology {name} has no version {version}"
            ))),
            Some(d) if d.status == STATUS_DRAFT => Err(GraphError::invalid(format!(
                "{name}@{version} is a draft: a change follows a published version"
            ))),
            Some(d) => Ok(d.version.clone()),
        }
    }

    /// The main goal the changes of a methodology start with (`registrysvc.Service.DefaultGoal`, ADR 0096): none for a
    /// methodology the registry does not know.
    pub fn default_goal(&self, reference: &str) -> String {
        self.get(reference).map(|d| d.goal.clone()).unwrap_or_default()
    }

    /// The lifecycle the changes of a methodology follow among the domains of a catalogue
    /// (`registrysvc.Service.Lifecycle`): none when it names none or the registry does not know it.
    pub fn lifecycle<'c>(&self, cat: &'c Catalog, reference: &str) -> Result<Option<&'c Arc<Lifecycle>>> {
        let Some(d) = self.get(reference) else { return Ok(None) };
        if d.lifecycle.is_empty() {
            return Ok(None);
        }
        match cat.named_lifecycle(&d.lifecycle, &d.namespace) {
            Some(l) => Ok(Some(l)),
            None => Err(GraphError::invalid(format!(
                "methodology {} names lifecycle {:?}, which no domain defines",
                d.name, d.lifecycle
            ))),
        }
    }

    /// Whether the changes of a methodology integrate the changes of another (`registrysvc.Service.Integrates`): not
    /// when the registry does not know the integrator.
    pub fn integrates(&self, integrator: &str, methodology: &str) -> bool {
        self.get(integrator)
            .is_some_and(|d| d.integrates_changes_of(methodology))
    }

    /// The guardian chain of a new root change of a methodology among the methodologies applicable to its project
    /// (`registrysvc.Service.Guardians`): those integrating it, followed upwards, outermost first, then itself.
    pub fn guardians(&self, reference: &str, applicable: &[String]) -> Vec<GuardianRef> {
        let (name, _) = split_ref(reference);
        let Some(own) = self.get(reference) else {
            return vec![GuardianRef {
                methodology: name.to_string(),
                version: String::new(),
            }];
        };
        let mut chain = vec![GuardianRef {
            methodology: name.to_string(),
            version: own.version.clone(),
        }];
        let mut seen = vec![name.to_string()];
        let mut cur = name.to_string();
        while chain.len() < MAX_CHAIN {
            let found = applicable
                .iter()
                .filter(|a| !seen.contains(a))
                .filter_map(|a| self.get(a))
                .find(|d| d.integrates_changes_of(&cur));
            let Some(m) = found else { break };
            chain.insert(
                0,
                GuardianRef {
                    methodology: m.name.clone(),
                    version: m.version.clone(),
                },
            );
            seen.push(m.name.clone());
            cur = m.name.clone();
        }
        chain
    }
}
