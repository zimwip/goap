//! An edit of the header of a change (Go's `Graph.UpdateChange`): its title, intent, goal, status and data, journaled
//! as a `change.updated` entry of its log, `change.edited` raised for what a reactive activity may look at again.
use serde_json::{Map, Value, json};

use super::{change::change_branch_origin, tx::Tx, whole::WholeChange, work::fold_change};
use crate::{
    GraphError, Result,
    change::{Change, ChangesFilter},
    impact::{LOG_FACT, LogEntry},
    jsontext,
    model::{ChangeId, MAIN_BRANCH, branch_of, namespace_of},
    store::WriteTx,
};

/// The fields an edit sets; none set leaves the field as it is.
#[derive(Debug, Clone, Default)]
pub struct ChangePatch {
    pub title: Option<String>,
    pub intent: Option<String>,
    pub goal: Option<String>,
    pub status: Option<String>,
    pub data: Option<Map<String, Value>>,
}

/// Whether a change may go from one status to another by an edit (`validStatusMove`).
fn valid_status_move(from: &str, to: &str) -> bool {
    match from {
        "draft" => to == "active" || to == "abandoned",
        "active" => to == "draft" || to == "abandoned",
        "committed" => to == "abandoned",
        _ => false,
    }
}

/// A field of an edit (`domain.HeaderValue`): its value before (left out when it had none) and after.
fn header_value(from: Option<Value>, to: Value) -> Value {
    match from.filter(|v| !v.is_null()) {
        Some(from) => json!({"from": from, "to": to}),
        None => json!({"to": to}),
    }
}

impl<S: WriteTx> Tx<'_, S> {
    /// Edits the header of a change. The change comes back as read, with the impacts and items of its view and the
    /// position of its log (the edit moves neither).
    pub async fn update_change(&mut self, id: &ChangeId, p: ChangePatch) -> Result<WholeChange> {
        self.lock_change(id).await?;
        let mut c = self.change_head(id).await?;
        // the view as read: no transition, so no freeze in force
        let view = fold_change(self.store, id).await?;
        let mut items = Vec::new();
        for l in self.store.log(id, 0, &[&format!("{LOG_FACT}.")]).await? {
            items.push(serde_json::from_str(&l.payload).map_err(GraphError::internal)?);
        }
        let mut edit = Map::new();
        let mut edited = Map::new();
        for (name, to, field) in [
            ("title", &p.title, &mut c.title),
            ("intent", &p.intent, &mut c.intent),
            ("goal", &p.goal, &mut c.goal),
        ] {
            if let Some(to) = to.as_ref().filter(|to| **to != *field) {
                let v = header_value(Some(Value::String(field.clone())), Value::String(to.clone()));
                edit.insert(name.into(), v.clone());
                edited.insert(name.into(), v);
                *field = to.clone();
            }
        }
        if let Some(to) = p.status.as_ref().filter(|to| **to != c.status) {
            if !valid_status_move(&c.status, to) {
                return Err(GraphError::conflict(format!(
                    "change {id} cannot go from {} to {to}",
                    c.status
                )));
            }
            edit.insert(
                "status".into(),
                header_value(Some(Value::String(c.status.clone())), Value::String(to.clone())),
            );
            c.status = to.clone();
            if c.status == "abandoned" {
                self.abandon_sub_changes(id).await?;
                self.abandon_own_branch(&c).await?;
            }
        }
        if let Some(patch) = p.data.filter(|m| !m.is_empty()) {
            let data = c.data.get_or_insert_with(Map::new);
            for (k, v) in patch {
                let old = data.get(&k).cloned();
                if old.clone().map(jsontext::floats) != Some(jsontext::floats(v.clone())) {
                    let hv = header_value(old, v.clone());
                    edit.insert(format!("data.{k}"), hv.clone());
                    edited.insert(format!("data.{k}"), hv);
                }
                data.insert(k, v);
            }
        }
        if !edit.is_empty() {
            let subject = edit.keys().cloned().collect::<Vec<_>>().join(",");
            let entry = LogEntry {
                seq: 0,
                id: self.env.new_id(),
                change: c.id.clone(),
                typ: "change.updated".into(),
                flow: String::new(),
                process: String::new(),
                execution: String::new(),
                subject,
                by: self.env.by.clone(),
                at: self.env.now(),
                causation: String::new(),
                payload: jsontext::to_string(&json!({ "fields": edit })),
            };
            self.append_log(&entry).await?;
        }
        self.put_change(&c).await?;
        if !edited.is_empty() {
            self.raise_edited(&c, view.seq, &edited);
        }
        Ok(WholeChange {
            change: c,
            impacts: view.fold.impacts.into_iter().map(|cn| (cn, None)).collect(),
            items,
            seq: view.seq,
        })
    }

    /// Abandons the open sub-changes of a change and their branches, recursively (`abandonSubChanges`).
    async fn abandon_sub_changes(&mut self, id: &ChangeId) -> Result<()> {
        let subs = self
            .store
            .change_heads(&ChangesFilter {
                parent: id.clone(),
                ..ChangesFilter::default()
            })
            .await?;
        for mut sc in subs
            .into_iter()
            .filter(|s| s.status != "applied" && s.status != "abandoned")
        {
            Box::pin(self.abandon_sub_changes(&sc.id)).await?;
            self.abandon_own_branch(&sc).await?;
            sc.status = "abandoned".into();
            self.put_change(&sc).await?;
        }
        Ok(())
    }

    /// Marks the branch of a change abandoned, when it has one of its own (`ownBranch`).
    async fn abandon_own_branch(&mut self, c: &Change) -> Result<()> {
        if branch_of(&c.branch) == MAIN_BRANCH {
            return Ok(());
        }
        let Some(mut b) = self.store.branch(namespace_of(&c.namespace), &c.branch).await? else {
            return Err(GraphError::not_found(format!("branch {}", c.branch)));
        };
        if b.origin == change_branch_origin(&c.id) {
            b.status = "abandoned".into();
            self.put_branch(&b).await?;
        }
        Ok(())
    }

    /// Moves the state of a change along a transition of its lifecycle (Go's `TransitionChange`, ADR 0058): with no
    /// registry, nothing resolves the lifecycle a change follows, so the move is refused once the change is known to be
    /// open.
    pub async fn transition_change(&mut self, id: &ChangeId) -> Result<Change> {
        let c = self.change_head(id).await?;
        if matches!(c.status.as_str(), "applied" | "abandoned" | "committed") {
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        if c.lifecycle.is_empty() {
            return Err(GraphError::invalid(format!("change {id} follows no lifecycle")));
        }
        Err(unresolved_lifecycle(&c))
    }

    /// Moves an open change to another version of its methodology (Go's `UpgradeChange`, ADR 0113): with no registry
    /// to resolve the versions, refused.
    pub async fn upgrade_change(&mut self, id: &ChangeId) -> Result<Change> {
        let c = self.change_head(id).await?;
        if c.methodology.is_empty() {
            return Err(GraphError::invalid(format!("change {id} follows no methodology")));
        }
        Err(GraphError::invalid(format!(
            "no registry resolves the versions of methodology {}",
            c.methodology
        )))
    }
}

/// The refusal of a change following a lifecycle, which no registry resolves here (Go's `changeLifecycle` with no
/// `Lifecycles`).
pub(super) fn unresolved_lifecycle(c: &Change) -> GraphError {
    GraphError::invalid(format!(
        "change {} follows lifecycle {}, which nothing resolves",
        c.id, c.lifecycle
    ))
}
