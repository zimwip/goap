//! The organisation and the tool layer as the hub reads them from the graph (`directory.go`).

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use goap_adapter as adapter;
use goap_ctx::Ctx;
use goap_domain::{
    BaselineID, Hierarchy, Link, MAIN_BRANCH, NAMESPACE_PLATFORM, Node, NodeID,
    STRUCTURE_ORGANISATION, Structures, TYPE_ADAPTER, TYPE_ADAPTER_DEF,
};
use goap_mcp as mcp;

use crate::Error;

/// Graph is the part of the graph the hub reads: the head of a branch and its graph, and the structures (ADR 0054:
/// the snapshot never names the organisation's types itself). Every `goap_graphapi::Port` is one.
pub use goap_access::Graph;

/// Baselines identifies the two namespace heads a Snapshot was built from.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Baselines {
    pub organisation: BaselineID,
    pub platform: BaselineID,
}

/// Snapshot is the organisation and the tool layer as of one baseline of each of the organisation and platform
/// namespaces: the unit hierarchy (part_of), the MCPs and adapter definitions of the platform namespace and the
/// adapters of the organisation namespace.
#[derive(Debug, Clone, Default)]
pub struct Snapshot {
    pub baselines: Baselines,
    /// Problems lists the nodes that could not be read (malformed adapter, adapter without unit, ...).
    pub problems: Vec<String>,

    /// the unit hierarchy of the organisation structure (ADR 0054)
    org: Hierarchy,
    /// unit keys
    units: HashMap<String, bool>,
    /// by name
    mcps: HashMap<String, mcp::Def>,
    /// adapter definitions by name
    defs: HashMap<String, adapter::Def>,
    /// unit -> mcp name -> adapter
    adapters: HashMap<String, HashMap<String, adapter::Instance>>,
}

/// Effective is an MCP a unit can use with the adapter that implements it.
#[derive(Debug, Clone, Default)]
pub struct Effective {
    pub mcp: mcp::Def,
    /// Adapter.unit is where it is defined.
    pub adapter: adapter::Instance,
    /// Inherited: defined by an ancestor unit.
    pub inherited: bool,
    /// Restriction is what the instances of the unit's chain restrict (ADR 0028).
    pub restriction: adapter::Restriction,
}

impl Effective {
    /// Allowed returns the MCP with only the tools the unit may call.
    pub fn allowed(&self) -> mcp::Def {
        self.restriction.apply(&self.mcp)
    }

    /// Usable reports whether the unit can call at least one tool of the MCP.
    pub fn usable(&self) -> bool {
        !self.allowed().tools.is_empty()
    }
}

/// BuildSnapshot reads the objects of the combined organisation and platform baseline graphs; the units are the nodes
/// of the organisation structure of st, their hierarchy its parent links (ADR 0054).
pub fn build_snapshot(
    st: &Structures,
    baselines: Baselines,
    nodes: &[Node],
    links: &[Link],
) -> Snapshot {
    let (nodes, links) = goap_access::in_force(nodes, links);
    let org = STRUCTURE_ORGANISATION;
    let org_ns = st.organisation().namespace;
    let mut s = Snapshot {
        baselines,
        org: st.hierarchy(org, &nodes, &links),
        ..Default::default()
    };
    let by_id: HashMap<&NodeID, &Node> = nodes.iter().map(|n| (&n.id, n)).collect();
    for n in &nodes {
        if n.namespace == org_ns && st.includes(org, &n.r#type) {
            s.units.insert(n.key.clone(), true);
        } else if n.namespace == NAMESPACE_PLATFORM && n.r#type == mcp::NODE_TYPE_MCP {
            match mcp::def_from_props(&n.properties).and_then(|d| d.validate().map(|_| d)) {
                Ok(d) => {
                    s.mcps.insert(d.name.clone(), d);
                }
                Err(e) => s.problems.push(format!("{}: {e}", n.key)),
            }
        } else if n.namespace == NAMESPACE_PLATFORM && n.r#type == TYPE_ADAPTER_DEF {
            match adapter::def_from_props(&n.properties).and_then(|d| d.validate().map(|_| d)) {
                Ok(d) => {
                    s.defs.insert(d.name.clone(), d);
                }
                Err(e) => s.problems.push(format!("{}: {e}", n.key)),
            }
        }
    }
    // an adapter instance is the unit's that owns it (ADR 0054: the owner of the version)
    for n in &nodes {
        if n.deleted || n.namespace != org_ns || n.r#type != TYPE_ADAPTER {
            continue;
        }
        let unit = match by_id.get(&n.owner) {
            Some(u) if st.includes(org, &u.r#type) => *u,
            _ => {
                s.problems.push(format!(
                    "{}: its owner {} is not a unit of the organisation",
                    n.key, n.owner
                ));
                continue;
            }
        };
        let a = match adapter::from_props(&unit.key, &n.properties) {
            Ok(a) => a,
            Err(e) => {
                s.problems.push(format!("{}: {e}", n.key));
                continue;
            }
        };
        let of_unit = s.adapters.entry(unit.key.clone()).or_default();
        if of_unit.contains_key(&a.mcp) {
            s.problems.push(format!(
                "{}: unit {} has several adapters for {}",
                n.key, unit.key, a.mcp
            ));
            continue;
        }
        of_unit.insert(a.mcp.clone(), a);
    }
    s
}

impl Snapshot {
    /// Unit returns the unit key, or the root of the organisation when empty.
    pub fn unit(&self, unit: &str) -> String {
        if unit.is_empty() {
            self.org.root().to_string()
        } else {
            unit.to_string()
        }
    }

    /// Chain returns the unit, its ancestors (part_of, nearest first) and, last, the default organisation, which
    /// every unit inherits from.
    pub fn chain(&self, unit: &str) -> Vec<String> {
        self.org.chain(unit)
    }

    /// Def returns an MCP definition.
    pub fn def(&self, name: &str) -> Option<&mcp::Def> {
        self.mcps.get(name)
    }

    /// Defs lists the MCPs by name.
    pub fn defs(&self) -> Vec<mcp::Def> {
        let mut out: Vec<mcp::Def> = self.mcps.values().cloned().collect();
        out.sort_by(|a, b| a.name.cmp(&b.name));
        out
    }

    /// AdapterDef returns an adapter definition.
    pub fn adapter_def(&self, name: &str) -> Option<&adapter::Def> {
        self.defs.get(name)
    }

    /// AdapterDefs lists the adapter definitions by name.
    pub fn adapter_defs(&self) -> Vec<adapter::Def> {
        let mut out: Vec<adapter::Def> = self.defs.values().cloned().collect();
        out.sort_by(|a, b| a.name.cmp(&b.name));
        out
    }

    /// Resolve returns the adapter of an MCP for a unit and whether it is inherited: the one of the nearest unit of
    /// its chain that names an adapter definition (instances that only restrict are skipped).
    pub fn resolve(&self, unit: &str, mcp_name: &str) -> Option<(adapter::Instance, bool)> {
        for (i, u) in self.chain(unit).iter().enumerate() {
            if let Some(a) = self.adapters.get(u).and_then(|m| m.get(mcp_name))
                && a.implements()
            {
                return Some((a.clone(), i > 0));
            }
        }
        None
    }

    /// Restriction returns what the instances of the unit's chain restrict of an MCP: they all add up, so that a
    /// unit cannot widen what an ancestor restricted.
    pub fn restriction(&self, unit: &str, mcp_name: &str) -> adapter::Restriction {
        let mut r = adapter::Restriction::default();
        for u in self.chain(unit) {
            if let Some(a) = self.adapters.get(&u).and_then(|m| m.get(mcp_name)) {
                r.add(a);
            }
        }
        r
    }

    /// Effective lists the MCPs implemented for a unit (an adapter exists in its chain for an MCP that exists), each
    /// with its resolved adapter and its restriction, by MCP name. A restricted MCP is listed even when no tool is
    /// left (see Effective::usable).
    pub fn effective(&self, unit: &str) -> Vec<Effective> {
        let mut out = Vec::new();
        for d in self.defs() {
            if let Some((a, inherited)) = self.resolve(unit, &d.name) {
                let restriction = self.restriction(unit, &d.name);
                out.push(Effective {
                    mcp: d,
                    adapter: a,
                    inherited,
                    restriction,
                });
            }
        }
        out
    }
}

#[derive(Default)]
struct State {
    cur: Option<Arc<Snapshot>>,
    /// the structures of the graph, asked once (ADR 0054: tagged by a frozen built-in domain)
    st: Option<Structures>,
}

/// Directory reads the snapshot of the head of the main branch, rebuilding it only when the head moved.
pub struct Directory {
    pub graph: Arc<dyn Graph>,
    state: tokio::sync::Mutex<State>,
}

impl Directory {
    /// A directory reading `graph`.
    pub fn new(graph: Arc<dyn Graph>) -> Self {
        Directory {
            graph,
            state: tokio::sync::Mutex::default(),
        }
    }

    /// structures asks the graph for its structures, once it answers.
    async fn structures(&self, ctx: &Ctx) -> Result<Structures, Error> {
        let mut st = self.state.lock().await;
        if st.st.is_none() {
            st.st = Some(self.graph.structures(ctx).await?);
        }
        Ok(st.st.clone().unwrap_or_default())
    }

    /// Scopes returns the scope of each MCP of the head of the platform namespace (ADR 0028).
    pub async fn scopes(&self, ctx: &Ctx) -> Result<BTreeMap<String, String>, Error> {
        let snap = self.snapshot(ctx).await?;
        Ok(snap
            .defs()
            .into_iter()
            .map(|d| {
                let scope = mcp::scope_of(&d.scope).to_string();
                (d.name, scope)
            })
            .collect())
    }

    /// Snapshot returns the current snapshot. A graph without any baseline yields an empty one.
    pub async fn snapshot(&self, ctx: &Ctx) -> Result<Arc<Snapshot>, Error> {
        let st = self.structures(ctx).await?;
        let org_head = head(self.graph.as_ref(), ctx, &st.organisation().namespace).await?;
        let plat_head = head(self.graph.as_ref(), ctx, NAMESPACE_PLATFORM).await?;
        let baselines = Baselines {
            organisation: org_head,
            platform: plat_head,
        };
        let mut state = self.state.lock().await;
        if let Some(cur) = &state.cur
            && cur.baselines == baselines
        {
            return Ok(cur.clone());
        }
        let mut nodes = Vec::new();
        let mut links = Vec::new();
        for id in [&baselines.organisation, &baselines.platform] {
            if !id.is_empty() {
                let (n, l) = self.graph.baseline_graph(ctx, id).await?;
                nodes.extend(n);
                links.extend(l);
            }
        }
        let snap = Arc::new(build_snapshot(&st, baselines, &nodes, &links));
        state.cur = Some(snap.clone());
        Ok(snap)
    }
}

/// The head baseline of the main branch of a namespace; none (the empty id) when the graph has none.
async fn head(g: &dyn Graph, ctx: &Ctx, namespace: &str) -> Result<BaselineID, Error> {
    match g.branch_head(ctx, namespace, MAIN_BRANCH).await {
        Ok(b) => Ok(b.id),
        Err(e) if e.is_not_found() => Ok(BaselineID::default()),
        Err(e) => Err(e.into()),
    }
}
