//! Opening and purging a change (Go's `Graph.CreateChange`, `Graph.PurgeChange`).
use graph_catalog::catalog::{STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};
use serde_json::{Map, Value};

use super::{events, tx::Tx};
use crate::{
    GraphError, Result,
    change::Change,
    lnk::Url,
    model::{BRANCH_OPEN, Branch, ChangeId, Node, branch_of, namespace_of},
    read,
    store::WriteTx,
};

pub const STATUS_DRAFT: &str = "draft";
pub const STATUS_ACTIVE: &str = "active";
pub const STATUS_ABANDONED: &str = "abandoned";
pub const STATUS_APPLIED: &str = "applied";
/// The intent of an option a change's own branch is opened with by default.
pub const INTENT_DERIVE: &str = "derive";

/// What opens a change (`graph.NewChange`, as the service fills it).
#[derive(Debug, Clone, Default)]
pub struct NewChange {
    pub title: String,
    pub intent: String,
    pub methodology: String,
    pub methodology_version: String,
    pub namespace: String,
    pub baseline_id: crate::model::BaselineId,
    pub branch: String,
    pub own_branch: bool,
    pub owner_org: String,
    pub project_id: String,
    pub data: Option<Map<String, Value>>,
    /// the change it is a sub-change of (ADR 0103)
    pub parent_id: ChangeId,
}

/// The name of the branch a change owns (`changeBranchName`).
pub fn change_branch_name(id: &ChangeId) -> String {
    let s = id.as_str();
    format!("change-{}", s.get(..8).unwrap_or(s))
}

/// The origin of the branch a change owns (`domain.ChangeBranchOrigin`).
pub fn change_branch_origin(id: &ChangeId) -> String {
    format!("change:{id}")
}

fn what(kind: &str) -> &'static str {
    if kind == STRUCTURE_ORGANISATION {
        "organisational unit"
    } else {
        "project"
    }
}

impl<S: WriteTx> Tx<'_, S> {
    /// The URL of the node of a structure of a key (`Graph.StructureURL`).
    pub fn structure_url(&self, kind: &str, key: &str) -> Result<Url> {
        let st = self
            .catalog()
            .structure(kind)
            .ok_or_else(|| GraphError::internal(format!("no {kind} structure in the catalogue")))?;
        let bare = st.typ.split_once('@').map_or(st.typ.as_str(), |(_, t)| t);
        Ok(Url::new(&st.namespace, bare, key))
    }

    /// What an input names as a node of a structure: its URL without its version, or its key (`Graph.StructureRef`).
    pub fn structure_ref(&self, kind: &str, s: &str) -> Result<Url> {
        if s.is_empty() {
            return Ok(Url::default());
        }
        if !s.starts_with("lnk:") {
            return self.structure_url(kind, s);
        }
        Url::parse(s).map(|u| u.logical()).map_err(GraphError::invalid)
    }

    /// Resolves the URL of a node of a structure (`Graph.structureNode`): the structure's source, one of its types,
    /// a node living on main.
    pub async fn structure_node(&self, kind: &str, u: &Url) -> Result<Node> {
        let cat = self.catalog();
        let st = cat
            .structure(kind)
            .ok_or_else(|| GraphError::internal(format!("no {kind} structure in the catalogue")))?;
        let what = what(kind);
        let typ = format!("{}@{}", st.namespace, u.typ);
        if u.is_zero() {
            return Err(GraphError::invalid(format!("no {what} named")));
        }
        if u.source != st.namespace {
            return Err(GraphError::invalid(format!(
                "{what} {u}: a {kind} is read in the source {}",
                st.namespace
            )));
        }
        if !cat.is_a(&typ, &st.typ) {
            return Err(GraphError::invalid(format!(
                "{what} {u}: {} is not a type of the {kind}",
                u.typ
            )));
        }
        let key = &u.key;
        let Some(id) = self.store.node_id_by_key(&st.namespace, key).await? else {
            return Err(GraphError::invalid(format!(
                "{what} {key:?} is not a node of namespace {}",
                st.namespace
            )));
        };
        let Some(n) = self.store.latest_on(&id, crate::model::MAIN_BRANCH).await? else {
            return Err(GraphError::invalid(format!("{what} {key:?} is not on main yet")));
        };
        if n.deleted {
            return Err(GraphError::invalid(format!("{what} {key:?} is deleted")));
        }
        if namespace_of(&n.namespace) != st.namespace || !cat.is_a(&n.typ, &st.typ) {
            return Err(GraphError::invalid(format!(
                "{what} {key:?} is a {}, not a {}",
                n.typ, st.typ
            )));
        }
        if !cat.is_a(&n.typ, &typ) {
            return Err(GraphError::invalid(format!("{what} {u} is a {}", n.typ)));
        }
        Ok(n)
    }

    /// The root of a structure.
    pub fn structure_root(&self, kind: &str) -> Result<String> {
        self.catalog()
            .structure(kind)
            .map(|s| s.root.clone())
            .ok_or_else(|| GraphError::internal(format!("no {kind} structure in the catalogue")))
    }

    /// Gives a change its unit and its project (Go's `scopeChange`): the root unit by default, a project always named,
    /// one spelling per node.
    pub(crate) async fn scope_change(&self, c: &mut Change) -> Result<()> {
        let org = STRUCTURE_ORGANISATION;
        let proj = STRUCTURE_PROJECT;
        if c.owner_org.is_zero() {
            c.owner_org = self.structure_url(org, &self.structure_root(org)?)?;
        }
        if c.project_id.is_zero() {
            return Err(GraphError::invalid("a change names the project it acts in"));
        }
        let unit = self
            .structure_node(org, &c.owner_org)
            .await
            .map_err(|e| e.context("owner of the change"))?;
        let project = self
            .structure_node(proj, &c.project_id)
            .await
            .map_err(|e| e.context("project of the change"))?;
        c.owner_org = self.structure_url(org, &unit.key)?;
        c.project_id = self.structure_url(proj, &project.key)?;
        Ok(())
    }

    /// Opens a root change (Go's `createChange` without a parent, a lifecycle or a guardian: the service has no
    /// registry, as `platform/cmd/graph`).
    pub async fn create_change(&mut self, mut inp: NewChange) -> Result<Change> {
        let org = STRUCTURE_ORGANISATION;
        let proj = STRUCTURE_PROJECT;
        let owner = self
            .structure_ref(org, &inp.owner_org)
            .map_err(|e| e.context("owner of the change"))?;
        let project = self
            .structure_ref(proj, &inp.project_id)
            .map_err(|e| e.context("project of the change"))?;
        let mut c = Change {
            id: ChangeId(self.env.new_id()),
            title: inp.title.clone(),
            intent: inp.intent.clone(),
            methodology: inp.methodology.clone(),
            methodology_version: inp.methodology_version.clone(),
            namespace: namespace_of(&inp.namespace).to_string(),
            status: STATUS_DRAFT.into(),
            baseline_id: inp.baseline_id.clone(),
            branch: branch_of(&inp.branch).to_string(),
            data: inp.data.take(),
            created_at: Some(self.env.now()),
            parent_id: inp.parent_id.clone(),
            owner_org: owner,
            project_id: project,
            ..Change::default()
        };
        self.prepare_sub_change(&mut c, &mut inp).await?;
        self.scope_change(&mut c).await?;
        if c.baseline_id.is_empty() && c.parent_id.is_empty() {
            c.baseline_id = read::branch_head(self.store, &c.namespace, &c.branch).await?.id;
        }
        let fork = read::header_of(self.store, &c.baseline_id).await?;
        if !fork.id.is_empty() && fork.namespace != c.namespace {
            return Err(GraphError::invalid(format!(
                "change acts on namespace {} but its reference baseline {} is of namespace {}",
                c.namespace, c.baseline_id, fork.namespace
            )));
        }
        let b = read::branch(self.store, &c.namespace, &c.branch).await?;
        if b.status != BRANCH_OPEN {
            return Err(GraphError::conflict(format!("branch {} is {}", b.name, b.status)));
        }
        if inp.own_branch {
            let mut own = Branch {
                ephemeral: true,
                name: change_branch_name(&c.id),
                namespace: c.namespace.clone(),
                parent: b.name.clone(),
                fork_baseline: fork.id.clone(),
                origin: change_branch_origin(&c.id),
                intent: INTENT_DERIVE.into(),
                status: BRANCH_OPEN.into(),
                created_at: Some(self.env.now()),
                ..Branch::default()
            };
            if b.ephemeral {
                (own.fork_baseline, own.fork_seq) = super::subchange::fork_point(self, &b).await?;
            }
            self.put_branch(&own).await?;
            c.branch = own.name;
        }
        self.put_change(&c).await?;
        self.raise(events::CHANGE_CREATED, &c, None, "", &[]);
        Ok(c)
    }

    /// Deletes a change that left nothing in the graph, with its log (Go's `PurgeChange`).
    pub async fn purge_change(&mut self, id: &ChangeId) -> Result<Change> {
        self.lock_change(id).await?;
        let c = self.change_head(id).await?;
        if ![STATUS_DRAFT, STATUS_ACTIVE, STATUS_ABANDONED].contains(&c.status.as_str()) {
            return Err(GraphError::conflict(format!(
                "change {id} is {}, it committed in the graph",
                c.status
            )));
        }
        if !c.result_baseline_id.is_empty() {
            return Err(GraphError::conflict(format!(
                "change {id} produced baseline {}",
                c.result_baseline_id
            )));
        }
        let subs = self
            .store
            .change_heads(&crate::change::ChangesFilter {
                parent: id.clone(),
                ..Default::default()
            })
            .await?;
        if !subs.is_empty() {
            return Err(GraphError::conflict(format!("change {id} has sub-changes")));
        }
        for typ in self.store.log_types(id).await? {
            let stream = typ.split('.').next().unwrap_or_default();
            if !matches!(stream, "fact" | "impact" | "change") {
                return Err(GraphError::conflict(format!(
                    "change {id} records an execution ({stream} entries in its log), kept for audit: abandon it instead"
                )));
            }
        }
        let mut branch = String::new();
        if branch_of(&c.branch) != crate::model::MAIN_BRANCH
            && let Some(own) = self.store.branch(&c.namespace, &c.branch).await?
            && own.origin == change_branch_origin(id)
        {
            if self.store.last_commit(&own.name).await? > 0 {
                return Err(GraphError::conflict(format!("change {id}: its branch holds commits")));
            }
            branch = own.name;
        }
        self.delete_change(id, &c.namespace, &branch).await?;
        self.raise(events::CHANGE_PURGED, &c, None, "", &[]);
        Ok(c)
    }
}
