//! The dependencies a change declares on others (ADR 0108, Go's `SetChangeDependencies`): written to its header, with
//! a `change.updated` entry of its log.
use std::collections::HashSet;

use serde_json::json;

use super::tx::Tx;
use crate::{GraphError, Result, change::Change, impact::LogEntry, model::ChangeId, store::WriteTx};

/// The field of a header edit naming the dependencies (`graph.HeaderDependsOn`).
pub const HEADER_DEPENDS_ON: &str = "dependsOn";

impl<S: WriteTx> Tx<'_, S> {
    /// Sets the changes an open change depends on: no itself, ancestor, sub-change, abandoned change or cycle. The
    /// change comes back with the position its view was read at, before the write.
    pub async fn set_change_dependencies(&mut self, id: &ChangeId, deps: &[ChangeId]) -> Result<(Change, i64)> {
        let mut want: Vec<ChangeId> = Vec::new();
        for d in deps {
            if !d.is_empty() && !want.contains(d) {
                want.push(d.clone());
            }
        }
        self.lock_change(id).await?;
        let mut c = self.change_head(id).await?;
        let seq = super::work::fold_change(self.store, id).await?.seq;
        if c.status != "draft" && c.status != "active" {
            return Err(GraphError::conflict(format!(
                "change {} is {}: its dependencies no longer change",
                c.id, c.status
            )));
        }
        if c.depends_on == want {
            return Ok((c, seq));
        }
        let ancestors = self.ancestor_ids(&c).await?;
        for d in &want {
            self.check_dependency(&c, &ancestors, d).await?;
        }
        let ids = |l: &[ChangeId]| l.iter().map(|i| i.0.clone()).collect::<Vec<_>>();
        let payload = json!({"fields": {HEADER_DEPENDS_ON: {"from": ids(&c.depends_on), "to": ids(&want)}}});
        let entry = LogEntry {
            seq: 0,
            id: self.env.new_id(),
            change: c.id.clone(),
            typ: "change.updated".into(),
            flow: String::new(),
            process: String::new(),
            execution: String::new(),
            subject: HEADER_DEPENDS_ON.into(),
            by: self.env.by.clone(),
            at: self.env.now(),
            causation: String::new(),
            payload: payload.to_string(),
        };
        self.append_log(&entry).await?;
        c.depends_on = want;
        self.put_change(&c).await?;
        Ok((c, seq))
    }

    /// The parents of a change up to its root, nearest first (`ancestorIDs`).
    async fn ancestor_ids(&mut self, c: &Change) -> Result<Vec<ChangeId>> {
        let mut out = Vec::new();
        let mut a = c.parent_id.clone();
        while !a.is_empty() && !out.contains(&a) {
            out.push(a.clone());
            a = self.change_head(&a).await?.parent_id;
        }
        Ok(out)
    }

    /// Whether change `c` may depend on `d` (`checkDependency`).
    async fn check_dependency(&mut self, c: &Change, ancestors: &[ChangeId], d: &ChangeId) -> Result<()> {
        if *d == c.id {
            return Err(GraphError::invalid("a change does not depend on itself"));
        }
        if ancestors.contains(d) {
            return Err(GraphError::invalid(format!(
                "change {d} is an ancestor of {}: it merges after it anyway",
                c.id
            )));
        }
        let dc = self.change_head(d).await?;
        if dc.status == "abandoned" {
            return Err(GraphError::conflict(format!(
                "change {d} is abandoned: it never commits"
            )));
        }
        if self.ancestor_ids(&dc).await?.contains(&c.id) {
            return Err(GraphError::invalid(format!(
                "change {d} is a sub-change of {}: a change already waits for its sub-changes",
                c.id
            )));
        }
        let mut seen = HashSet::new();
        let mut todo = vec![d.clone()];
        let mut i = 0;
        while i < todo.len() {
            let x = todo[i].clone();
            i += 1;
            if x == c.id {
                return Err(GraphError::invalid(format!(
                    "change {d} depends, directly or not, on {}: a dependency never makes a cycle",
                    c.id
                )));
            }
            if !seen.insert(x.clone()) {
                continue;
            }
            todo.extend(self.change_head(&x).await?.depends_on);
        }
        Ok(())
    }
}
