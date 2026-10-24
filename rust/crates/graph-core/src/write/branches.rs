//! The branches people open, close and describe (Go's `CreateBranch`, `SetBranchStatus`, `SetBranchDescription`):
//! rows of the store, written by no change.
use super::tx::Tx;
use crate::{
    GraphError, Result,
    model::{BRANCH_ABANDONED, BRANCH_MERGED, BRANCH_OPEN, BaselineId, Branch, MAIN_BRANCH, branch_of, namespace_of},
    read,
    store::WriteTx,
};

/// The statuses a branch takes.
const STATUSES: [&str; 3] = [BRANCH_OPEN, BRANCH_MERGED, BRANCH_ABANDONED];

/// A branch to open: forked from a baseline of its namespace (empty: the empty state).
#[derive(Debug, Clone, Default)]
pub struct NewBranch {
    pub name: String,
    pub namespace: String,
    pub from: BaselineId,
    pub origin: String,
    pub description: String,
}

/// Opens a branch forked from a baseline.
pub async fn create_branch<S: WriteTx>(tx: &mut Tx<'_, S>, inp: NewBranch) -> Result<Branch> {
    if inp.name.is_empty() || inp.name == MAIN_BRANCH || inp.name.contains([' ', '\t', '\n']) {
        return Err(GraphError::invalid(format!("invalid branch name {:?}", inp.name)));
    }
    let ns = namespace_of(&inp.namespace);
    if tx.store.branch(ns, &inp.name).await?.is_some() {
        return Err(GraphError::conflict(format!(
            "branch {} already exists in namespace {ns}",
            inp.name
        )));
    }
    let fork = read::header_of(tx.store, &inp.from).await?;
    if !fork.id.is_empty() && fork.namespace != ns {
        return Err(GraphError::invalid(format!(
            "baseline {} is of namespace {}, not {ns}",
            inp.from, fork.namespace
        )));
    }
    let b = Branch {
        name: inp.name,
        namespace: ns.to_string(),
        parent: branch_of(&fork.branch).to_string(),
        fork_baseline: fork.id.clone(),
        head: fork.id,
        origin: inp.origin,
        status: BRANCH_OPEN.into(),
        created_at: Some(tx.env.now()),
        description: inp.description,
        ..Branch::default()
    };
    tx.put_branch(&b).await?;
    Ok(b)
}

/// Closes a branch (merged, abandoned) or opens it again.
pub async fn set_branch_status<S: WriteTx>(
    tx: &mut Tx<'_, S>,
    namespace: &str,
    name: &str,
    status: &str,
) -> Result<()> {
    if !STATUSES.contains(&status) {
        return Err(GraphError::invalid(format!("invalid branch status {status:?}")));
    }
    let mut b = tx
        .store
        .branch(namespace_of(namespace), name)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("branch {name}")))?;
    b.status = status.to_string();
    tx.put_branch(&b).await
}

/// Sets the description of a branch; main too, its row written at its first use.
pub async fn set_branch_description<S: WriteTx>(
    tx: &mut Tx<'_, S>,
    namespace: &str,
    name: &str,
    description: &str,
) -> Result<()> {
    let mut b = read::branch(tx.store, namespace, name).await?;
    b.description = description.to_string();
    tx.put_branch(&b).await
}
