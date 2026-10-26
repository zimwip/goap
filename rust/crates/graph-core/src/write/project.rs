//! A change moving to another project (Go's `MoveChange`, ADR 0091) and a change taking a methodology (Go's
//! `SetChangeMethodology`, ADR 0096): header edits journaled in a `change.updated` entry, with the gates the Go
//! service plugs from the organisation (`graphsvc.ProjectMoveGate`, `MethodologyGate`) for a platform service.
use graph_catalog::catalog::STRUCTURE_PROJECT;
use std::collections::HashMap;

use serde_json::{Map, Value, json};

use super::{
    attach::{chain_under, chain_value, own_ref},
    change::{STATUS_ABANDONED, STATUS_ACTIVE, STATUS_APPLIED, STATUS_DRAFT},
    tx::Tx,
};
use crate::{
    GraphError, Result,
    access::Directory,
    change::{Change, ChangesFilter},
    impact::LogEntry,
    lnk::Url,
    methodologies::Declared,
    model::ChangeId,
    store::WriteTx,
};

/// The field of a header edit recording a move to another project (`graph.HeaderProjectID`).
pub const HEADER_PROJECT_ID: &str = "projectId";
/// The field of a header edit recording the methodology a change took (`graph.HeaderMethodology`).
pub const HEADER_METHODOLOGY: &str = "methodology";
/// The field of a header edit recording the version of its methodology a change follows (`graph.HeaderMethodologyVersion`).
pub const HEADER_METHODOLOGY_VERSION: &str = "methodologyVersion";

impl<S: WriteTx> Tx<'_, S> {
    /// Moves a root change, draft or active and having committed nothing, with its open sub-changes to another
    /// project, the guardian chain of each computed again for it (ADR 0106). The change comes back with the position its
    /// view was read at.
    /// `gate` judges the caller on each project a change of the family leaves or enters (Go's `ProjectMoveGate`).
    pub async fn move_change(
        &mut self,
        id: &ChangeId,
        to: &str,
        gate: &(dyn Fn(&Change, &str) -> Result<()> + Sync),
    ) -> Result<(Change, i64)> {
        let project = self
            .structure_ref(STRUCTURE_PROJECT, to)
            .map_err(|e| e.context(format!("move change {id}")))?;
        if project.is_zero() {
            return Err(GraphError::invalid(format!("move change {id}: a project is required")));
        }
        let node = self
            .structure_node(STRUCTURE_PROJECT, &project)
            .await
            .map_err(|e| e.context("target project"))?;
        let project = self.structure_url(STRUCTURE_PROJECT, &node.key)?;
        let family = self.movable(id, &project).await?;
        // the gate: the caller on both projects, then the methodologies
        let dir = Directory::read(self.store, self.catalog()).await?;
        for c in &family {
            for p in [c.project_id.key.as_str(), node.key.as_str()] {
                gate(c, p)?;
            }
            if c.methodology.is_empty() {
                continue;
            }
            for p in [c.project_id.key.as_str(), node.key.as_str()] {
                if !dir.applicable(p).contains(&c.methodology) {
                    return Err(GraphError::invalid(format!(
                        "methodology {} of change {} does not apply to project {p} (a change moves between projects that both apply its methodology)",
                        c.methodology, c.id
                    )));
                }
            }
        }
        // the guardian chain of the root computed again for the new project (ADR 0106), its sub-changes' from it
        let root = &family[0];
        let root_chain = match root.methodology.is_empty() {
            true => Vec::new(),
            false => {
                let declared = Declared::read(self.store).await?;
                self.root_chain(&declared, &root.methodology_ref(), &project).await?
            }
        };
        self.lock_change(id).await?;
        let seq = super::work::fold_change(self.store, id).await?.seq;
        let mut moved_to: HashMap<ChangeId, Change> = HashMap::new();
        for mut c in family {
            let mut fields = json!({HEADER_PROJECT_ID: {"from": c.project_id.to_string(), "to": project.to_string()}});
            let mut chain = c.guardians.clone();
            if !c.methodology.is_empty() {
                if &c.id == id {
                    chain = root_chain.clone();
                } else if let Some(p) = moved_to.get(&c.parent_id) {
                    chain = chain_under(p, &c.methodology, own_ref(&c));
                }
            }
            if chain != c.guardians {
                fields[super::attach::HEADER_GUARDIANS] =
                    json!({"from": chain_value(&c.guardians), "to": chain_value(&chain)});
            }
            self.header_entry(&c.id, fields).await?;
            c.project_id = project.clone();
            c.guardians = chain;
            self.put_change(&c).await?;
            moved_to.insert(c.id.clone(), c);
        }
        let moved = moved_to.remove(id).unwrap_or_default();
        Ok((moved, seq))
    }

    /// The change and its open sub-changes, parents first, when the rules of the graph let them move (`movable`).
    async fn movable(&mut self, id: &ChangeId, project: &Url) -> Result<Vec<Change>> {
        let c = self.change_head(id).await?;
        if !c.parent_id.is_empty() {
            return Err(GraphError::invalid(format!(
                "change {id} is a sub-change: its project is its parent's, move {}",
                c.parent_id
            )));
        }
        self.view(id).await?;
        let commits = (c.status == STATUS_DRAFT || c.status == STATUS_ACTIVE)
            && self.views[id]
                .fold
                .impacts
                .iter()
                .any(|cn| cn.flow.is_empty() && cn.committed.is_some());
        if c.status != STATUS_DRAFT && c.status != STATUS_ACTIVE || commits {
            let status = if commits {
                "committed on its branch".to_string()
            } else {
                c.status.clone()
            };
            return Err(GraphError::conflict(format!(
                "change {id} is {status}: only a draft or active change that committed nothing moves to another project"
            )));
        }
        if &c.project_id == project {
            return Err(GraphError::invalid(format!(
                "change {id} already acts in project {project}"
            )));
        }
        let mut family = vec![c];
        let mut i = 0;
        while i < family.len() {
            let parent = family[i].id.clone();
            i += 1;
            let mut subs: Vec<Change> = self
                .store
                .change_heads(&ChangesFilter {
                    parent,
                    ..Default::default()
                })
                .await?
                .into_iter()
                .filter(|s| s.status != STATUS_APPLIED && s.status != STATUS_ABANDONED)
                .collect();
            subs.sort_by(|a, b| a.id.0.cmp(&b.id.0));
            family.extend(subs);
        }
        Ok(family)
    }

    /// Gives an open change that follows no methodology the one named, as a creation would have (Go's
    /// `SetChangeMethodology`): the methodology applying to the project of the change, its latest published version,
    /// its main goal when the change has none, its lifecycle starting in its initial state, the guardian chain of its
    /// project or under its parent.
    pub async fn set_change_methodology(&mut self, id: &ChangeId, methodology: &str) -> Result<(Change, i64)> {
        if methodology.is_empty() {
            return Err(GraphError::invalid("a methodology is required"));
        }
        let mut c = self.change_head(id).await?;
        adoptable(&c)?;
        let dir = Directory::read(self.store, self.catalog()).await?;
        if !dir.applicable(&c.project_id.key).contains(&methodology.to_string()) {
            return Err(GraphError::invalid(format!(
                "methodology {methodology} does not apply to project {}, the project of change {id}",
                c.project_id
            )));
        }
        let m = self
            .resolve_methodology(methodology, "", &c.parent_id, &c.project_id)
            .await?;
        if m.version.is_empty() {
            return Err(GraphError::invalid(format!("methodology {methodology} is unknown")));
        }
        self.lock_change(id).await?;
        let seq = super::work::fold_change(self.store, id).await?.seq;
        let mut fields = json!({HEADER_METHODOLOGY: {"from": "", "to": methodology}});
        if !m.version.is_empty() {
            fields[HEADER_METHODOLOGY_VERSION] = json!({"from": c.methodology_version, "to": m.version});
        }
        if !m.guardians.is_empty() {
            fields[super::attach::HEADER_GUARDIANS] =
                json!({"from": chain_value(&c.guardians), "to": chain_value(&m.guardians)});
        }
        self.header_entry(id, fields).await?;
        c.methodology = methodology.to_string();
        c.methodology_version = m.version;
        if c.goal.is_empty() {
            c.goal = m.goal;
        }
        c.guardians = m.guardians;
        if c.lifecycle.is_empty() {
            (c.lifecycle, c.state) = (m.lifecycle, m.initial);
        }
        self.put_change(&c).await?;
        Ok((c, seq))
    }

    /// Journals an edit of the header of a change (Go's `headerEntry`): a `change.updated` entry naming its fields.
    pub(crate) async fn header_entry(&mut self, id: &ChangeId, fields: Value) -> Result<()> {
        let mut names: Vec<&String> = fields.as_object().map(Map::keys).into_iter().flatten().collect();
        names.sort();
        let subject = names.iter().map(|s| s.as_str()).collect::<Vec<_>>().join(",");
        let entry = LogEntry {
            seq: 0,
            id: self.env.new_id(),
            change: id.clone(),
            typ: "change.updated".into(),
            flow: String::new(),
            process: String::new(),
            execution: String::new(),
            subject,
            by: self.env.by.clone(),
            at: self.env.now(),
            causation: String::new(),
            payload: json!({"fields": fields}).to_string(),
        };
        self.append_log(&entry).await?;
        Ok(())
    }
}

/// A change may take a methodology: open, and following none yet (`adoptable`).
fn adoptable(c: &Change) -> Result<()> {
    if !c.methodology.is_empty() {
        return Err(GraphError::conflict(format!(
            "change {} follows methodology {} already",
            c.id, c.methodology
        )));
    }
    if c.status == STATUS_DRAFT || c.status == STATUS_ACTIVE {
        return Ok(());
    }
    Err(GraphError::conflict(format!(
        "change {} is {}: it no longer takes a methodology",
        c.id, c.status
    )))
}
