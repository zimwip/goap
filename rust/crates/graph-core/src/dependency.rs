//! Dependencies between changes (ADR 0108, Go's `graph/graph/dependency.go`): a dependency is satisfied when its work
//! reached main or the branch the dependent change merges into.
use std::collections::HashSet;

use crate::{
    GraphError, Result,
    change::{Change, ChangesFilter},
    model::ChangeId,
    store::ReadTx,
};

/// A dependency of a change, as the change sees it (`domain.Dependency`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Dependency {
    pub id: ChangeId,
    pub title: String,
    pub methodology: String,
    pub status: String,
    pub state: String,
    pub parent_id: ChangeId,
    pub satisfied: bool,
    pub reason: String,
}

impl Dependency {
    fn of(c: &Change) -> Dependency {
        Dependency {
            id: c.id.clone(),
            title: c.title.clone(),
            methodology: c.methodology.clone(),
            status: c.status.clone(),
            state: c.state.clone(),
            parent_id: c.parent_id.clone(),
            ..Dependency::default()
        }
    }
}

/// The dependencies of a change, those it waits for and the order its open sub-changes merge in.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ChangeDependencies {
    pub depends_on: Vec<Dependency>,
    pub dependents: Vec<Dependency>,
    pub order: Vec<Dependency>,
}

const OPEN: [&str; 3] = ["draft", "active", "committed"];

async fn head<T: ReadTx>(tx: &T, id: &ChangeId) -> Result<Change> {
    tx.change(id)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("change {id}")))
}

/// Whether the work of change `d` reached the branch `c` merges into (`dependencyOf`).
pub async fn dependency_of<T: ReadTx>(tx: &T, c: &Change, d: &ChangeId) -> Result<Dependency> {
    let dc = head(tx, d).await?;
    let mut out = Dependency::of(&dc);
    match dc.status.as_str() {
        "abandoned" => {
            out.reason = "abandoned: it never commits".into();
            return Ok(out);
        }
        "applied" => {}
        _ => {
            out.reason = "not closed yet".into();
            return Ok(out);
        }
    }
    let mut x = dc;
    while x.status == "applied" && !x.parent_id.is_empty() {
        if x.parent_id == c.parent_id {
            out.satisfied = true; // merged into the change c merges into
            return Ok(out);
        }
        x = head(tx, &x.parent_id).await?;
    }
    if x.status == "applied" {
        out.satisfied = true; // committed on main
        return Ok(out);
    }
    out.reason = format!("merged into change {} ({}), which has not committed", x.id, x.title);
    Ok(out)
}

/// The dependencies of a change, in the order it names them (`dependenciesOf`).
pub async fn dependencies_of<T: ReadTx>(tx: &T, c: &Change) -> Result<Vec<Dependency>> {
    let mut out = Vec::with_capacity(c.depends_on.len());
    for d in &c.depends_on {
        out.push(dependency_of(tx, c, d).await?);
    }
    Ok(out)
}

/// The order changes merge in, each after the ones of the list it depends on, else the oldest first
/// (`domain.DependencyOrder`).
pub fn dependency_order(cs: &[Change]) -> Vec<Change> {
    let within: HashSet<&ChangeId> = cs.iter().map(|c| &c.id).collect();
    let mut rest = cs.to_vec();
    rest.sort_by(|a, b| a.created_at.cmp(&b.created_at).then_with(|| a.id.cmp(&b.id)));
    let mut done: HashSet<ChangeId> = HashSet::new();
    let mut out = Vec::with_capacity(cs.len());
    while !rest.is_empty() {
        let i = rest
            .iter()
            .position(|c| c.depends_on.iter().all(|d| !within.contains(d) || done.contains(d)))
            .unwrap_or(0); // a cycle: the oldest first
        let c = rest.remove(i);
        done.insert(c.id.clone());
        out.push(c);
    }
    out
}

/// What a change depends on, the open changes depending on it and the merge order of its open sub-changes
/// (`ChangeDependencies`).
pub async fn change_dependencies<T: ReadTx>(tx: &T, id: &ChangeId) -> Result<ChangeDependencies> {
    let c = head(tx, id).await?;
    let mut out = ChangeDependencies {
        depends_on: dependencies_of(tx, &c).await?,
        ..ChangeDependencies::default()
    };
    let open: Vec<String> = OPEN.iter().map(|s| s.to_string()).collect();
    let dependents = tx
        .change_heads(&ChangesFilter {
            depends_on: id.clone(),
            status: open.clone(),
            ..ChangesFilter::default()
        })
        .await?;
    for x in &dependents {
        let dep = dependency_of(tx, x, id).await?;
        out.dependents.push(Dependency {
            satisfied: dep.satisfied,
            reason: dep.reason,
            ..Dependency::of(x)
        });
    }
    let subs = tx
        .change_heads(&ChangesFilter {
            parent: id.clone(),
            status: open,
            ..ChangesFilter::default()
        })
        .await?;
    for s in dependency_order(&subs) {
        let mut o = Dependency {
            satisfied: true,
            ..Dependency::of(&s)
        };
        if let Some(d) = dependencies_of(tx, &s).await?.into_iter().find(|d| !d.satisfied) {
            o.satisfied = false;
            o.reason = format!("waits for {} ({})", d.id, d.title);
        }
        out.order.push(o);
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn change(id: &str, at: i64, deps: &[&str]) -> Change {
        Change {
            id: ChangeId(id.into()),
            created_at: chrono::DateTime::from_timestamp(at, 0),
            depends_on: deps.iter().map(|d| ChangeId(d.to_string())).collect(),
            ..Change::default()
        }
    }

    #[test]
    fn a_change_merges_after_what_it_depends_on() {
        let cs = [change("a", 1, &["c"]), change("b", 2, &["x"]), change("c", 3, &[])];
        let order: Vec<_> = dependency_order(&cs).into_iter().map(|c| c.id.0).collect();
        assert_eq!(order, ["b", "c", "a"]);
        let cycle = [change("a", 1, &["b"]), change("b", 2, &["a"])];
        let order: Vec<_> = dependency_order(&cycle).into_iter().map(|c| c.id.0).collect();
        assert_eq!(order, ["a", "b"]);
    }
}
