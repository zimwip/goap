//! The methodologies the registry declares, as the graph reads them to instantiate what their changes follow (Go's
//! `registrysvc.Service` held by `platform/cmd/graph` over the platform graph): the version a change follows, the goal
//! it starts with, the lifecycle it moves along and its guardian chain (ADR 0058, 0096, 0106, 0113). The registry keeps
//! each version as a `MethodologyVersion` header and one node per element in the `methodology` namespace (ADR 0023);
//! what is read here is the header, its goals and processes and the link naming its main goal, at the head of main.
use std::collections::HashMap;

use chrono::{DateTime, Utc};
use serde::Deserialize;
use serde_json::{Map, Value, json};

use graph_catalog::{Catalog, def::Lifecycle};
use graph_script::conditions::Set;
use std::sync::Arc;

use crate::{
    GraphError, Result,
    change::GuardianRef,
    conditions::{
        derive::{self, Derived, Gate},
        stored,
    },
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

/// What a methodology requires of the changes it integrates (`methodology.Guardian`).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Guardian {
    /// asked at each commit of a change it integrates
    pub commit: String,
    /// asked at a transition of the lifecycle of such a change, by transition name
    pub transitions: HashMap<String, String>,
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
    /// what it requires of the changes it integrates (ADR 0106)
    pub guardian: Option<Guardian>,
}

impl Declaration {
    /// The guard this methodology adds to each commit of a change it integrates (`Methodology.CommitGuard`): an
    /// attached integrator when its integration of that methodology is exclusive, then its own commit guard.
    pub fn commit_guard(&self, of: &str) -> String {
        let mut parts = Vec::new();
        if self.integration_of(of).is_some_and(|i| i.exclusive) {
            parts.push("integrator.attached".to_string());
        }
        if let Some(g) = &self.guardian
            && !g.commit.trim().is_empty()
        {
            parts.push(format!("({})", g.commit));
        }
        parts.join(" && ")
    }

    /// The guard it adds to a transition of the lifecycle of a change it integrates (`Methodology.TransitionGuard`).
    pub fn transition_guard(&self, name: &str) -> String {
        self.guardian
            .as_ref()
            .and_then(|g| g.transitions.get(name))
            .cloned()
            .unwrap_or_default()
    }

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
            let guardian = n
                .props
                .as_ref()
                .and_then(|p| p.get("guardian"))
                .and_then(|v| serde_json::from_value(v.clone()).ok());
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
                guardian,
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

    /// The methodology a change follows, instantiated from the nodes the registry declares (`registrysvc.Service`
    /// compiling a stored version): its definition, the conditions it compiles to and the lifecycle its changes move
    /// along, gates merged in. None when the registry does not know the reference.
    pub async fn instance<T: ReadTx>(&self, tx: &T, reference: &str) -> Result<Option<Instance>> {
        let Some(d) = self.get(reference) else { return Ok(None) };
        let header = version_key(&d.name, &d.version);
        let (nodes, links) = elements(tx, &header).await?;
        let ns: Vec<stored::Node> = nodes
            .iter()
            .map(|n| stored::Node {
                key: &n.key,
                r#type: &n.typ,
                props: n.props.as_ref().unwrap_or(&EMPTY),
            })
            .collect();
        let ls: Vec<stored::Link> = links
            .iter()
            .filter_map(|l| {
                let to = l.to_node()?;
                let from = nodes
                    .iter()
                    .find(|n| n.id == l.from.id && n.version == l.from.version)?;
                let to = nodes.iter().find(|n| n.id == to.id)?;
                Some(stored::Link {
                    from: &from.key,
                    r#type: &l.typ,
                    to: &to.key,
                    props: l.props.as_ref(),
                })
            })
            .collect();
        let Some(definition) = stored::definition(&header, &ns, &ls) else {
            return Ok(None);
        };
        let derived = derive::derive(&definition);
        let set = Set::compile(&derived.conditions)
            .map_err(|e| GraphError::invalid(format!("conditions of methodology {}@{}: {e}", d.name, d.version)))?;
        Ok(Some(Instance {
            declaration: d.clone(),
            definition,
            derived,
            set,
        }))
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

/// The key of the node of a methodology version (`registrysvc.MethodologyVersionKey`).
pub fn version_key(name: &str, version: &str) -> String {
    format!("MV:{name}@{version}")
}

static EMPTY: std::sync::LazyLock<Map<String, Value>> = std::sync::LazyLock::new(Map::new);

/// The nodes of a stored version (its header and its elements) and the links leaving them, at the head of main of the
/// namespace of the methodologies, or of the platform graph for a content graph.
async fn elements<T: ReadTx>(tx: &T, header: &str) -> Result<(Vec<Node>, Vec<Link>)> {
    let prefix = format!("{header}/");
    let keep = |n: &Node| !n.deleted && (n.key == header || n.key.starts_with(&prefix));
    if let Some(p) = tx.platform() {
        let nodes: Vec<Node> = p.methodology.nodes.iter().filter(|n| keep(n)).cloned().collect();
        let held: Vec<&NodeId> = nodes.iter().map(|n| &n.id).collect();
        let links = p
            .methodology
            .links
            .iter()
            .filter(|l| held.contains(&&l.from.id))
            .cloned()
            .collect();
        return Ok((nodes, links));
    }
    let head = read::branch_head(tx, NAMESPACE_METHODOLOGY, MAIN_BRANCH).await?;
    let at = read::select(tx, &head, None).await?;
    let refs: Vec<NodeRef> = at.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect();
    let mut nodes = Vec::new();
    for typ in stored::node_types() {
        nodes.extend(tx.nodes_at(&refs, typ).await?.into_iter().filter(keep));
    }
    let held: Vec<NodeRef> = nodes.iter().map(Node::node_ref).collect();
    let links = tx.links_from(&held, None).await?;
    Ok((nodes, links))
}

/// A methodology version as the graph instantiates it: what the registry declares, the definition read back from its
/// nodes, what that definition compiles to and the conditions of its changes.
pub struct Instance {
    pub declaration: Declaration,
    pub definition: Map<String, Value>,
    pub derived: Derived,
    pub set: Set,
}

impl Instance {
    /// The lifecycle the changes of the version follow, with the criteria and the freezes of its gates merged into the
    /// transitions they take (`registrysvc.Service.Lifecycle` through `WithGates`, ADR 0132 §2, §4): the graph enforces
    /// them as it does the ones the domain declares. None when the methodology names no lifecycle.
    pub fn lifecycle(&self, cat: &Catalog) -> Result<Option<Lifecycle>> {
        let d = &self.declaration;
        if d.lifecycle.is_empty() {
            return Ok(None);
        }
        let Some(found) = cat.named_lifecycle(&d.lifecycle, &d.namespace) else {
            return Err(GraphError::invalid(format!(
                "methodology {} names lifecycle {:?}, which no domain defines",
                d.name, d.lifecycle
            )));
        };
        let mut out = Lifecycle::clone(found);
        for g in &self.derived.gates {
            let mut taken = false;
            for t in out.transitions.iter_mut().filter(|t| t.name == g.transition) {
                taken = true;
                let (vetos, objectives) = criteria(g);
                t.vetos.extend(vetos);
                t.objectives.extend(objectives);
                if let Some(f) = &g.freezes
                    && !freezes_empty(f)
                {
                    t.freezes = Some(match &t.freezes {
                        Some(a) => merge_freezes(a, f),
                        None => f.clone(),
                    });
                }
            }
            if !taken {
                return Err(GraphError::invalid(format!(
                    "gate {} takes transition {}, which lifecycle {} has not",
                    g.path, g.transition, out.name
                )));
            }
        }
        Ok(Some(out))
    }
}

/// The criteria a gate adds to its transition (`GateInfo.Criteria`, ADR 0132 §2): its checklist items as vetos or
/// objectives over the world state, and the decision of the gate, a veto.
fn criteria(g: &Gate) -> (Vec<Value>, Vec<Value>) {
    let (mut vetos, mut objectives) = (Vec::new(), Vec::new());
    for it in &g.items {
        let world = format!("world[{}]", q(&it.condition));
        let expr = if it.when.is_empty() {
            world
        } else {
            format!("!({}) || {world}", it.when)
        };
        let c = json!({"name": it.name, "expr": expr, "description": it.description});
        if it.veto { vetos.push(c) } else { objectives.push(c) }
    }
    vetos.push(json!({"name": "decision", "description": "the decision of the gate said go",
        "expr": format!(
            "decisionPoints.exists(d, d.id == change.decision && d.status == \"decided\" && d.option == {} && \"gate\" in d.policy && d.policy.gate == {})",
            q("go"), q(&g.path))}));
    (vetos, objectives)
}

/// A JSON string as Go's `%q` writes one.
fn q(s: &str) -> String {
    Value::from(s).to_string()
}

fn freezes_strings(f: &Value, k: &str) -> Vec<Value> {
    f.get(k)
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default()
        .into_iter()
        .filter(Value::is_string)
        .collect()
}

fn freezes_flag(f: &Value, k: &str) -> bool {
    f.get(k).and_then(Value::as_bool).unwrap_or_default()
}

/// Whether a freeze freezes nothing beyond the phase of the state left (`Freezes.Empty`).
pub(crate) fn freezes_empty(f: &Value) -> bool {
    freezes_strings(f, "fields").is_empty() && freezes_strings(f, "types").is_empty() && !freezes_flag(f, "keepPhase")
}

/// Two freezes of one transition, added up (`mergeFreezes`).
fn merge_freezes(a: &Value, b: &Value) -> Value {
    let add = |k: &str| {
        let mut out = freezes_strings(a, k);
        for v in freezes_strings(b, k) {
            if !out.contains(&v) {
                out.push(v);
            }
        }
        out
    };
    json!({"fields": add("fields"), "types": add("types"),
        "transitions": freezes_flag(a, "transitions") || freezes_flag(b, "transitions"),
        "keepPhase": freezes_flag(a, "keepPhase") || freezes_flag(b, "keepPhase")})
}
