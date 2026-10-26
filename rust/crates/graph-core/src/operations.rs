//! The operations a person performs on a change and the roles a methodology allows each one to (ADR 0103, 0138): Go's
//! `contracts/operations` catalogue and `Methodology.OperationRoles`. The roles are read from the version of the
//! methodology the change follows as the methodology registry stores it in the graph (ADR 0023: a `MethodologyVersion`
//! header and one `Role` element per role, in the `methodology` namespace), at the head of main.
use chrono::{DateTime, Utc};
use serde_json::Value;

use crate::{
    Result,
    model::{MAIN_BRANCH, Node, NodeRef},
    read,
    store::ReadTx,
};

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

/// The namespace of the methodologies (`registrysvc.NamespaceMethodology`).
const NAMESPACE: &str = "methodology";
const TYPE_VERSION: &str = "methodology@MethodologyVersion";
const TYPE_ROLE: &str = "methodology@Role";
const STATUS_PUBLISHED: &str = "published";
const STATUS_DELETED: &str = "deleted";

/// The roles a version of a methodology allows an operation to, in the order of the definition (`registrysvc`'s
/// `GraphStore.Get` then `Methodology.OperationRoles`): a role naming no operation allows them all. The reference is
/// `<name>@<version>`, or a bare name for its latest published version; none when the registry holds no such version.
pub async fn operation_roles<T: ReadTx>(tx: &T, reference: &str, code: &str) -> Result<Option<Vec<String>>> {
    let (name, version) = match reference.rfind('@') {
        Some(i) if i > 0 => (&reference[..i], &reference[i + 1..]),
        _ => (reference, ""),
    };
    // the methodologies of another graph (a content graph, ADR 0154 §9), else the head of main of their namespace
    let held = tx.platform().map(|p| &p.methodology.nodes);
    let refs: Vec<NodeRef> = match held {
        Some(_) => Vec::new(),
        None => {
            let head = read::branch_head(tx, NAMESPACE, MAIN_BRANCH).await?;
            let at = read::select(tx, &head, None).await?;
            at.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect()
        }
    };
    let of_type = async |typ: &str| -> Result<Vec<Node>> {
        match held {
            Some(ns) => Ok(ns.iter().filter(|n| n.typ == typ).cloned().collect()),
            None => tx.nodes_at(&refs, typ).await,
        }
    };
    let text = |n: &Node, k: &str| -> String {
        n.props
            .as_ref()
            .and_then(|p| p.get(k))
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    let headers = of_type(TYPE_VERSION).await?;
    let mut found = headers.iter().filter(|n| !n.deleted && text(n, "name") == name);
    let header = if version.is_empty() {
        let published_at = |n: &Node| {
            DateTime::parse_from_rfc3339(&text(n, "publishedAt"))
                .map(|t| t.with_timezone(&Utc))
                .unwrap_or_default()
        };
        found
            .filter(|n| text(n, "status") == STATUS_PUBLISHED)
            .fold(None, |best: Option<&Node>, n| match best {
                Some(b) if published_at(n) <= published_at(b) => Some(b),
                _ => Some(n),
            })
    } else {
        found.find(|n| text(n, "version") == version && text(n, "status") != STATUS_DELETED)
    };
    let Some(header) = header else { return Ok(None) };
    let prefix = format!("{}/role/", header.key); // `<header>/<kind>/<name>`, as the registry keys its elements
    let mut roles: Vec<_> = of_type(TYPE_ROLE)
        .await?
        .into_iter()
        .filter(|n| {
            !n.deleted
                && n.key.starts_with(&prefix)
                && !n.key[prefix.len()..].contains('/')
                && n.props.as_ref().and_then(|p| p.get("removed")).and_then(Value::as_bool) != Some(true)
        })
        .collect();
    // by key, then by position (the definition's order)
    roles.sort_by(|a, b| a.key.cmp(&b.key));
    let position = |n: &Node| {
        n.props
            .as_ref()
            .and_then(|p| p.get("position"))
            .and_then(Value::as_f64)
            .unwrap_or_default()
    };
    roles.sort_by(|a, b| position(a).total_cmp(&position(b)));
    Ok(Some(
        roles
            .iter()
            .filter(|r| {
                let ops: Vec<String> = r
                    .props
                    .as_ref()
                    .and_then(|p| p.get("operations"))
                    .and_then(Value::as_array)
                    .map(|l| l.iter().filter_map(Value::as_str).map(str::to_string).collect())
                    .unwrap_or_default();
                ops.is_empty() || allows(&ops, code)
            })
            .map(|r| text(r, "name"))
            .collect(),
    ))
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
