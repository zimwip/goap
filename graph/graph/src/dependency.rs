//! Port of `dependency.go`: dependencies between changes (ADR 0108). A change names the changes whose work must have
//! reached the branch it merges into before it merges (`Change::depends_on`). The merge refuses a change with a
//! dependency not satisfied (check_merge), the guards see them (FACET_DEPENDENCIES), and the open sub-changes of a
//! change merge in their dependency order (`dependency_order`, change_dependencies).

use std::collections::{BTreeMap, HashSet, VecDeque};

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ABANDONED, CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_COMMITTED, CHANGE_DRAFT, Change,
    ChangeID, Dependency, HeaderValue, dependency_order,
};
use goap_operations as operations;
use serde_json::{Value, json};

use crate::api::ChangeDependencies;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// HeaderDependsOn is the field of a change.updated log entry recording a change of the dependencies of a change.
pub const HEADER_DEPENDS_ON: &str = "dependsOn";

impl Graph {
    /// SetChangeDependencies replaces the changes a change depends on. The change is open; each dependency exists, is
    /// another change, neither one of its ancestors nor one of its sub-changes (a change already waits for its
    /// sub-changes), and the relation never makes a cycle (Invalid). An abandoned dependency is refused (Conflict): it
    /// never lands. The change is journaled in a change.updated entry (dependsOn {from, to}); no entry when nothing
    /// changes.
    ///
    /// goap:operation change.depend doer
    pub async fn set_change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        deps: &[ChangeID],
    ) -> Result<Change> {
        self.allow(ctx, id, operations::CHANGE_DEPEND).await?;
        let mut want: Vec<ChangeID> = Vec::new();
        for d in deps {
            if !d.is_empty() && !want.contains(d) {
                want.push(d.clone());
            }
        }
        let mut tx = self.change_tx(ctx, id).await?;
        let mut c = tx.change(ctx, id).await?;
        if c.status != CHANGE_DRAFT && c.status != CHANGE_ACTIVE {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {} is {}: its dependencies no longer change",
                    c.id, c.status
                ),
            ));
        }
        if c.depends_on == want {
            tx.commit(ctx).await?;
            return Ok(c);
        }
        let ancestors = ancestor_ids(ctx, &mut tx, &c).await?;
        for d in &want {
            check_dependency(ctx, &mut tx, &c, &ancestors, d).await?;
        }
        let mut fields = BTreeMap::new();
        fields.insert(
            HEADER_DEPENDS_ON.to_string(),
            HeaderValue {
                from: id_strings(&c.depends_on),
                to: id_strings(&want),
            },
        );
        self.header_entry(ctx, &mut tx, &c.id, fields).await?;
        c.depends_on = want;
        tx.put_change(ctx, c.clone()).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }

    /// ChangeDependencies reads the dependencies of a change, the open changes depending on it and the merge order of
    /// its open sub-changes.
    pub async fn change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
    ) -> Result<ChangeDependencies> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let mut out = ChangeDependencies {
            depends_on: dependencies_of(ctx, &mut tx, &c).await?,
            ..Default::default()
        };
        let all = tx.changes(ctx).await?;
        let mut subs = Vec::new();
        for x in all {
            let open = x.status == CHANGE_DRAFT
                || x.status == CHANGE_ACTIVE
                || x.status == CHANGE_COMMITTED;
            if !open {
                continue;
            }
            if x.depends_on.contains(id) {
                let mut dep = dependency_of(ctx, &mut tx, &x, id).await?;
                dep.id = x.id.clone();
                dep.title = x.title.clone();
                dep.methodology = x.methodology.clone();
                dep.status = x.status.clone();
                dep.state = x.state.clone();
                dep.parent_id = x.parent_id.clone();
                out.dependents.push(dep);
            }
            if &x.parent_id == id {
                subs.push(x);
            }
        }
        for s in dependency_order(&subs) {
            let deps = dependencies_of(ctx, &mut tx, &s).await?;
            let mut o = Dependency {
                id: s.id.clone(),
                title: s.title.clone(),
                methodology: s.methodology.clone(),
                status: s.status.clone(),
                state: s.state.clone(),
                parent_id: s.parent_id.clone(),
                satisfied: true,
                ..Default::default()
            };
            if let Some(d) = deps.iter().find(|d| !d.satisfied) {
                o.satisfied = false;
                o.reason = format!("waits for {} ({})", d.id, d.title);
            }
            out.order.push(o);
        }
        tx.commit(ctx).await?;
        Ok(out)
    }
}

/// checkDependency checks that change c may depend on change d.
pub(crate) async fn check_dependency(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    c: &Change,
    ancestors: &[ChangeID],
    d: &ChangeID,
) -> Result<()> {
    if d == &c.id {
        return Err(Error::wrap(
            ErrorKind::Invalid,
            "a change does not depend on itself",
        ));
    }
    if ancestors.contains(d) {
        return Err(Error::wrap(
            ErrorKind::Invalid,
            format!(
                "change {d} is an ancestor of {}: it merges after it anyway",
                c.id
            ),
        ));
    }
    let dc = tx.change(ctx, d).await?;
    if dc.status == CHANGE_ABANDONED {
        return Err(Error::wrap(
            ErrorKind::Conflict,
            format!("change {d} is abandoned: it never lands"),
        ));
    }
    let up = ancestor_ids(ctx, tx, &dc).await?;
    if up.contains(&c.id) {
        return Err(Error::wrap(
            ErrorKind::Invalid,
            format!(
                "change {d} is a sub-change of {}: a change already waits for its sub-changes",
                c.id
            ),
        ));
    }
    // no cycle: c is not reached from d by the dependencies already stored
    let mut seen = HashSet::new();
    let mut todo = VecDeque::from([d.clone()]);
    while let Some(x) = todo.pop_front() {
        if x == c.id {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "change {d} depends, directly or not, on {}: a dependency never makes a cycle",
                    c.id
                ),
            ));
        }
        if !seen.insert(x.clone()) {
            continue;
        }
        let xc = tx.change(ctx, &x).await?;
        todo.extend(xc.depends_on);
    }
    Ok(())
}

/// ancestorIDs are the ancestors of a change, its parent first.
pub(crate) async fn ancestor_ids(ctx: &Ctx, tx: &mut dyn Tx, c: &Change) -> Result<Vec<ChangeID>> {
    let mut out: Vec<ChangeID> = Vec::new();
    let mut a = c.parent_id.clone();
    while !a.is_empty() && !out.contains(&a) {
        out.push(a.clone());
        a = tx.change(ctx, &a).await?.parent_id;
    }
    Ok(out)
}

/// idStrings is a list of change ids as a field of a header edit.
fn id_strings(ids: &[ChangeID]) -> Value {
    json!(ids.iter().map(|id| id.to_string()).collect::<Vec<_>>())
}

/// dependencyOf is change d as a dependency of change c: whether its work has reached the branch c merges into. The
/// work of an applied change is on the branch of its parent, that of an applied parent on its own parent's, and so on:
/// it is satisfied when it reached main (every change of its chain applied) or c's parent.
pub(crate) async fn dependency_of(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    c: &Change,
    d: &ChangeID,
) -> Result<Dependency> {
    let dc = tx.change(ctx, d).await?;
    let mut out = Dependency {
        id: dc.id.clone(),
        title: dc.title.clone(),
        methodology: dc.methodology.clone(),
        status: dc.status.clone(),
        state: dc.state.clone(),
        parent_id: dc.parent_id.clone(),
        ..Default::default()
    };
    if dc.status == CHANGE_ABANDONED {
        out.reason = "abandoned: it never lands".into();
        return Ok(out);
    }
    if dc.status != CHANGE_APPLIED {
        out.reason = "not merged yet".into();
        return Ok(out);
    }
    let mut x = dc;
    while x.status == CHANGE_APPLIED && !x.parent_id.is_empty() {
        if x.parent_id == c.parent_id {
            out.satisfied = true; // merged into the change c merges into
            return Ok(out);
        }
        x = tx.change(ctx, &x.parent_id).await?;
    }
    if x.status == CHANGE_APPLIED {
        out.satisfied = true; // landed on main
        return Ok(out);
    }
    out.reason = format!(
        "merged into change {} ({}), which has not landed",
        x.id, x.title
    );
    Ok(out)
}

/// dependenciesOf are the dependencies of a change, as it sees them.
pub(crate) async fn dependencies_of(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    c: &Change,
) -> Result<Vec<Dependency>> {
    let mut out = Vec::with_capacity(c.depends_on.len());
    for d in &c.depends_on {
        out.push(dependency_of(ctx, tx, c, d).await?);
    }
    Ok(out)
}

/// checkDependencies refuses the merge of a change one of whose dependencies is not satisfied (ADR 0108).
pub(crate) fn check_dependencies(c: &Change, deps: &[Dependency]) -> Result<()> {
    let wait: Vec<String> = deps
        .iter()
        .filter(|d| !d.satisfied)
        .map(|d| format!("{} ({}): {}", d.id, d.title, d.reason))
        .collect();
    if !wait.is_empty() {
        return Err(crate::Error::wrap(
            crate::ErrorKind::Conflict,
            format!(
                "change {} depends on changes whose work has not reached the branch it merges into: {}",
                c.id,
                wait.join("; ")
            ),
        ));
    }
    Ok(())
}
