//! Part of `dependency.go` (G2): how a change sees its dependencies (ADR 0108), the facet of its blackboard and the check of the merge. Setting
//! them (`SetChangeDependencies`) and `ChangeDependencies` belong to slice G3 (see PORTING.md).

use goap_ctx::Ctx;
use goap_domain::{CHANGE_ABANDONED, CHANGE_APPLIED, Change, ChangeID, Dependency};

use crate::Result;
use crate::repo::Tx;

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
