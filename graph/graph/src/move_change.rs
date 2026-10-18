//! A change moves to another project (ADR 0091). (`move` is a Rust keyword: the module is
//! `move_change`.)

use std::collections::HashMap;

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ACTIVE, CHANGE_DRAFT, Change, ChangeID, GuardianRef, HeaderEdit, HeaderValue,
    STRUCTURE_PROJECT, header_entry,
};
use serde_json::Value;

use crate::attach::{HEADER_GUARDIANS, guardians_value, own_ref};
use crate::rebase::has_commits;
use crate::repo::Tx;
use crate::subchange::open_sub_changes;
use crate::transition::chain_under;
use crate::{Error, ErrorKind, Graph, Result};

/// HeaderProjectID is the field of a change.updated log entry that records a move to another project.
pub const HEADER_PROJECT_ID: &str = "projectId";

impl Graph {
    /// MoveChange moves a change to another project (ADR 0091): the root change of a family, still draft or active,
    /// and with it its open sub-changes, in one transaction. A sub-change cannot move alone: its project is its
    /// parent's. The target must be a project and not the one the change is in, and the project move gate (when set)
    /// must accept the move. The nodes keep their projects (the project of a node never changes), the nodes the change
    /// creates take the new project when it lands. The guardian chain of each moved change is computed again for the
    /// new project (ADR 0106). Each moved change logs a change.updated entry with a projectId field {from, to} (and a
    /// guardians field when its chain changed), by the caller. It returns the change as moved.
    pub async fn move_change(&self, ctx: &Ctx, id: &ChangeID, project: &str) -> Result<Change> {
        if project.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("move change {id}: a project is required"),
            ));
        }
        let family = {
            let mut tx = self.begin(ctx).await?;
            let f = self.movable(ctx, &mut tx, id, project).await?;
            tx.commit(ctx).await?;
            f
        };
        if let Some(gate) = &self.project_move_gate {
            gate.check(ctx, &family, project).await?;
        }
        // the guardian chain of the root is computed again for the new project (ADR 0106), its sub-changes' from it
        let mut root_chain: Vec<GuardianRef> = Vec::new();
        let root = &family[0];
        if let Some(lcs) = &self.lifecycles
            && !root.methodology.is_empty()
        {
            root_chain = lcs.guardians(ctx, &root.methodology_ref(), project).await?;
        }
        let mut tx = self.change_tx(ctx, id).await?;
        let again = self.movable(ctx, &mut tx, id, project).await?;
        let changed = again.len() != family.len()
            || again
                .iter()
                .zip(&family)
                .any(|(a, f)| a.id != f.id || a.project_id != f.project_id);
        if changed {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} changed while it was checked for a move"),
            ));
        }
        let mut moved_to: HashMap<ChangeID, Change> = HashMap::new();
        let mut moved = Change::default();
        for mut c in again {
            let mut fields = std::collections::BTreeMap::new();
            fields.insert(
                HEADER_PROJECT_ID.to_string(),
                HeaderValue {
                    from: Value::String(c.project_id.clone()),
                    to: Value::String(project.to_string()),
                },
            );
            let mut chain = c.guardians.clone();
            if self.lifecycles.is_some() && !c.methodology.is_empty() {
                if &c.id == id {
                    chain = root_chain.clone();
                } else if let Some(p) = moved_to.get(&c.parent_id) {
                    chain = chain_under(p, &c.methodology, own_ref(&c).as_ref());
                }
            }
            if chain != c.guardians {
                fields.insert(
                    HEADER_GUARDIANS.to_string(),
                    HeaderValue {
                        from: guardians_value(&c.guardians),
                        to: guardians_value(&chain),
                    },
                );
            }
            let e = header_entry(
                &self.new_id(),
                &c.id,
                &self.caller_of(ctx),
                self.now(),
                &HeaderEdit { fields },
            )?;
            tx.append_log(ctx, e).await?;
            c.project_id = project.to_string();
            c.guardians = chain;
            moved_to.insert(c.id.clone(), c.clone());
            tx.put_change(ctx, c.clone()).await?;
            if &c.id == id {
                moved = c;
            }
        }
        tx.commit(ctx).await?;
        Ok(moved)
    }

    /// movable checks the rules of the graph for a move and returns the change followed by the open sub-changes under
    /// it, parents first.
    pub(crate) async fn movable(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        project: &str,
    ) -> Result<Vec<Change>> {
        let c = tx.change(ctx, id).await?;
        if !c.parent_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "change {id} is a sub-change: its project is its parent's, move {}",
                    c.parent_id
                ),
            ));
        }
        if (c.status != CHANGE_DRAFT && c.status != CHANGE_ACTIVE) || has_commits(&c) {
            let status = if has_commits(&c) {
                "committed on its branch".to_string()
            } else {
                c.status.to_string()
            };
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {id} is {status}: only a draft or active change that committed nothing moves to another project"
                ),
            ));
        }
        if c.project_id == project {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("change {id} already acts in project {project}"),
            ));
        }
        self.structure_node(ctx, tx, STRUCTURE_PROJECT, project)
            .await
            .map_err(|e| e.context("target project"))?;
        let mut family = vec![c];
        let mut i = 0;
        while i < family.len() {
            let mut subs = open_sub_changes(ctx, tx, &family[i].id).await?;
            subs.sort_by(|a, b| a.id.cmp(&b.id));
            family.extend(subs);
            i += 1;
        }
        Ok(family)
    }
}
