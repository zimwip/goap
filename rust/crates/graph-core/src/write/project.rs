//! A change moving to another project (Go's `MoveChange`, ADR 0091) and a change taking a methodology (Go's
//! `SetChangeMethodology`, ADR 0096): header edits journaled in a `change.updated` entry, with the gates the Go
//! service plugs from the organisation (`graphsvc.ProjectMoveGate`, `MethodologyGate`) for a platform service.
use graph_catalog::catalog::STRUCTURE_PROJECT;
use serde_json::{Map, Value, json};

use super::{
    change::{STATUS_ABANDONED, STATUS_ACTIVE, STATUS_APPLIED, STATUS_DRAFT},
    tx::Tx,
};
use crate::{
    GraphError, Result,
    access::Directory,
    change::{Change, ChangesFilter},
    impact::LogEntry,
    lnk::Url,
    model::ChangeId,
    store::WriteTx,
};

/// The field of a header edit recording a move to another project (`graph.HeaderProjectID`).
pub const HEADER_PROJECT_ID: &str = "projectId";
/// The field of a header edit recording the methodology a change took (`graph.HeaderMethodology`).
pub const HEADER_METHODOLOGY: &str = "methodology";

impl<S: WriteTx> Tx<'_, S> {
    /// Moves a root change, draft or active and having committed nothing, with its open sub-changes to another
    /// project. With no registry no guardian chain is computed again. The change comes back with the position its
    /// view was read at.
    pub async fn move_change(&mut self, id: &ChangeId, to: &str) -> Result<(Change, i64)> {
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
        // the gate (a platform service by itself: the methodologies only)
        let dir = Directory::read(self.store, self.catalog()).await?;
        for c in &family {
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
        self.lock_change(id).await?;
        let seq = super::work::fold_change(self.store, id).await?.seq;
        let mut moved = Change::default();
        for mut c in family {
            let fields = json!({HEADER_PROJECT_ID: {"from": c.project_id.to_string(), "to": project.to_string()}});
            self.header_entry(&c.id, fields).await?;
            c.project_id = project.clone();
            self.put_change(&c).await?;
            if &c.id == id {
                moved = c;
            }
        }
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

    /// Gives an open change that follows no methodology the one named. With no registry no version, goal, lifecycle
    /// or guardian is resolved: the methodology only, applying to the project of the change.
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
        self.lock_change(id).await?;
        let seq = super::work::fold_change(self.store, id).await?.seq;
        self.header_entry(id, json!({HEADER_METHODOLOGY: {"from": "", "to": methodology}}))
            .await?;
        c.methodology = methodology.to_string();
        self.put_change(&c).await?;
        Ok((c, seq))
    }

    /// Journals an edit of the header of a change (Go's `headerEntry`): a `change.updated` entry naming its fields.
    async fn header_entry(&mut self, id: &ChangeId, fields: Value) -> Result<()> {
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
