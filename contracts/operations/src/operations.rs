//! The catalogue and its queries (`operations.go`).
use std::fmt;

use crate::operations_gen::CATALOG;

/// Classes an operation by who performs it: the doer changes the work (drafts, impacts, commits), the reviewer judges
/// it (accepts or rejects an impact) and never changes it.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum Kind {
    Doer,
    Reviewer,
}

impl Kind {
    /// The name of the kind, as a role lists it: "doer" or "reviewer".
    pub fn as_str(self) -> &'static str {
        match self {
            Kind::Doer => "doer",
            Kind::Reviewer => "reviewer",
        }
    }

    /// The kind of a name, if it is one.
    pub fn parse(name: &str) -> Option<Kind> {
        kinds().into_iter().find(|k| k.as_str() == name)
    }
}

impl fmt::Display for Kind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// Lists the kinds of operation.
pub fn kinds() -> Vec<Kind> {
    vec![Kind::Doer, Kind::Reviewer]
}

/// An entry of the catalogue: its code, its kind and the method of the graph that performs it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Operation {
    pub code: &'static str,
    pub kind: Kind,
    pub method: &'static str,
    pub doc: &'static str,
}

/// Lists the operations of the catalogue, by code.
pub fn all() -> Vec<Operation> {
    CATALOG.to_vec()
}

/// Returns the operation of a code.
pub fn lookup(code: &str) -> Option<Operation> {
    CATALOG.iter().find(|o| o.code == code).copied()
}

/// Reports whether name is the code of an operation or a kind of operation, what a role may list.
pub fn known(name: &str) -> bool {
    lookup(name).is_some() || Kind::parse(name).is_some()
}

/// Resolves what a role lists (codes and kinds) to the codes of the operations it allows, sorted; the names it does
/// not know are left out (the compiler reports them).
pub fn expand<S: AsRef<str>>(names: &[S]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for n in names {
        let n = n.as_ref();
        for o in CATALOG {
            if (o.code == n || o.kind.as_str() == n) && !out.iter().any(|c| c == o.code) {
                out.push(o.code.to_string());
            }
        }
    }
    out.sort();
    out
}

/// Reports whether what a role lists allows the operation of code: the code itself or its kind.
pub fn allows<S: AsRef<str>>(names: &[S], code: &str) -> bool {
    match lookup(code) {
        Some(o) => names
            .iter()
            .any(|n| n.as_ref() == code || n.as_ref() == o.kind.as_str()),
        None => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{IMPACT_REVIEW, IMPACT_UPDATE, IMPACT_WITHDRAW};
    use std::collections::HashSet;

    #[test]
    fn catalogue() {
        let mut seen = HashSet::new();
        for o in all() {
            assert!(seen.insert(o.code), "duplicate {}", o.code);
            assert!(
                kinds().contains(&o.kind) && !o.method.is_empty(),
                "operation {o:?}"
            );
        }
        let o = lookup(IMPACT_REVIEW);
        assert!(
            matches!(o, Some(o) if o.kind == Kind::Reviewer),
            "impact.review: {o:?}"
        );
        assert!(
            known("reviewer") && known(IMPACT_UPDATE) && !known("impact.nope"),
            "known"
        );
        assert_eq!(expand(&["reviewer"]), vec![IMPACT_REVIEW.to_string()]);
        assert!(
            allows(&["doer"], IMPACT_WITHDRAW)
                && !allows(&["reviewer"], IMPACT_WITHDRAW)
                && allows(&[IMPACT_WITHDRAW], IMPACT_WITHDRAW),
            "allows"
        );
    }
}
