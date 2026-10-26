//! The operations a person performs on a change and the roles a methodology allows each one to (ADR 0103, 0138): Go's
//! `contracts/operations` catalogue, the roles of the version of the methodology the change follows asked of its
//! guardian (ADR 0168).
use crate::{Result, store::ReadTx};

/// The operations of the catalogue with their kind (`operations.All`, generated from the annotations of the Go graph).
pub const CATALOGUE: [(&str, &str); 23] = [
    ("change.attach", DOER),
    ("change.close", DOER),
    ("change.commit", DOER),
    ("change.depend", DOER),
    ("change.detach", DOER),
    ("change.merge", DOER),
    ("change.methodology", DOER),
    ("change.rebase", DOER),
    ("change.upgrade", DOER),
    ("impact.cancel", DOER),
    ("impact.checkout", DOER),
    ("impact.create", DOER),
    ("impact.link.create", DOER),
    ("impact.link.delete", DOER),
    ("impact.link.update", DOER),
    ("impact.merge", DOER),
    ("impact.propose", DOER),
    ("impact.resolve", DOER),
    ("impact.review", REVIEWER),
    ("impact.split", DOER),
    ("impact.transition", DOER),
    ("impact.update", DOER),
    ("impact.withdraw", DOER),
];

/// The kind of the operations that do the work of a change.
pub const DOER: &str = "doer";
/// The kind of the operations that judge it.
pub const REVIEWER: &str = "reviewer";

pub const CHANGE_ATTACH: &str = "change.attach";
pub const CHANGE_CLOSE: &str = "change.close";
pub const CHANGE_COMMIT: &str = "change.commit";
pub const CHANGE_DEPEND: &str = "change.depend";
pub const CHANGE_DETACH: &str = "change.detach";
pub const CHANGE_MERGE: &str = "change.merge";
pub const CHANGE_METHODOLOGY: &str = "change.methodology";
pub const CHANGE_REBASE: &str = "change.rebase";
pub const CHANGE_UPGRADE: &str = "change.upgrade";
pub const IMPACT_CANCEL: &str = "impact.cancel";
pub const IMPACT_CHECKOUT: &str = "impact.checkout";
pub const IMPACT_CREATE: &str = "impact.create";
pub const IMPACT_LINK_CREATE: &str = "impact.link.create";
pub const IMPACT_LINK_DELETE: &str = "impact.link.delete";
pub const IMPACT_LINK_UPDATE: &str = "impact.link.update";
pub const IMPACT_MERGE: &str = "impact.merge";
pub const IMPACT_PROPOSE: &str = "impact.propose";
pub const IMPACT_RESOLVE: &str = "impact.resolve";
pub const IMPACT_REVIEW: &str = "impact.review";
pub const IMPACT_SPLIT: &str = "impact.split";
pub const IMPACT_TRANSITION: &str = "impact.transition";
pub const IMPACT_UPDATE: &str = "impact.update";
pub const IMPACT_WITHDRAW: &str = "impact.withdraw";

/// Whether a list of operation codes and kinds allows an operation (`operations.Allows`).
pub fn allows(names: &[String], code: &str) -> bool {
    CATALOGUE
        .iter()
        .find(|(c, _)| *c == code)
        .is_some_and(|(c, kind)| names.iter().any(|n| n == c || n == kind))
}

/// The roles a version of a methodology allows an operation to (`graphsvc.OperationAuthorizer` over
/// `registrysvc.Service.OperationRoles`), asked of the guardian (ADR 0168): a role naming no operation allows them all.
/// The reference is `<name>@<version>`, or a bare name for its latest published version; none when the methodology
/// restricts nothing (one the registry does not hold). With no guardian, no role: the administrators' (ADR 0138).
pub async fn operation_roles<T: ReadTx>(tx: &T, reference: &str, code: &str) -> Result<Option<Vec<String>>> {
    match tx.guardian() {
        Some(g) if !reference.is_empty() => g.operation_roles(reference, code).await,
        Some(_) => Ok(None),
        None => Ok(Some(Vec::new())),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn codes_and_kinds_allow_an_operation() {
        let names = |l: &[&str]| l.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert!(allows(&names(&["doer"]), IMPACT_UPDATE));
        assert!(!allows(&names(&["doer"]), IMPACT_REVIEW));
        assert!(allows(&names(&["reviewer"]), IMPACT_REVIEW));
        assert!(allows(&names(&["change.commit"]), CHANGE_COMMIT));
        assert!(!allows(&names(&["change.commit"]), "unknown"));
    }
}
