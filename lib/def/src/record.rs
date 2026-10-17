//! The stored versions of a domain, as the domain registry serves them (ADR 0125 §3).

use std::borrow::Cow;
use std::collections::HashMap;
use std::fmt;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Deserializer, Serialize, Serializer};

use crate::domain::Domain;

/// Status of a stored version (a domain, or a methodology, which share the lifecycle): a draft is editable until it is
/// published (immutable); a published version is archived instead of deleted.
#[derive(Clone, Default, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct Status(Cow<'static, str>);

impl Status {
    /// A status known at compile time.
    pub const fn from_static(s: &'static str) -> Self {
        Status(Cow::Borrowed(s))
    }

    pub fn new(s: impl Into<String>) -> Self {
        Status(Cow::Owned(s.into()))
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

pub const STATUS_DRAFT: Status = Status::from_static("draft");
pub const STATUS_PUBLISHED: Status = Status::from_static("published");
pub const STATUS_ARCHIVED: Status = Status::from_static("archived");

impl fmt::Display for Status {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl fmt::Debug for Status {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Debug::fmt(&*self.0, f)
    }
}

impl std::ops::Deref for Status {
    type Target = str;
    fn deref(&self) -> &str {
        &self.0
    }
}

impl From<&str> for Status {
    fn from(s: &str) -> Self {
        Status::new(s)
    }
}

impl From<String> for Status {
    fn from(s: String) -> Self {
        Status::new(s)
    }
}

impl PartialEq<str> for Status {
    fn eq(&self, o: &str) -> bool {
        self.as_str() == o
    }
}

impl PartialEq<&str> for Status {
    fn eq(&self, o: &&str) -> bool {
        self.as_str() == *o
    }
}

impl Serialize for Status {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&self.0)
    }
}

impl<'de> Deserialize<'de> for Status {
    fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        Ok(Status::new(
            Option::<String>::deserialize(d)?.unwrap_or_default(),
        ))
    }
}

/// Record is a stored domain version, as the domain registry serves it (ADR 0125 §3).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Record {
    pub domain: Domain,
    pub status: Status,
    pub created_at: Option<DateTime<Utc>>,
    pub updated_at: Option<DateTime<Utc>>,
    /// None while the version is not published (Go's zero time).
    pub published_at: Option<DateTime<Utc>>,
    pub updated_by: String,
    /// Builtin marks a domain shipped with the platform (`builtin_domains`): published, frozen, not stored.
    pub builtin: bool,
}

/// Latest keeps the latest version of each domain of rs, in their first order: the latest published one if any, the
/// latest draft otherwise.
pub fn latest(rs: &[Record]) -> Vec<Record> {
    let mut latest: HashMap<&str, &Record> = HashMap::new();
    let mut order: Vec<&str> = Vec::new();
    for r in rs {
        let cur = latest.get(r.domain.name.as_str()).copied();
        if cur.is_none() {
            order.push(&r.domain.name);
        }
        let better = match cur {
            None => true,
            Some(cur) => {
                (r.status == STATUS_PUBLISHED
                    && (cur.status != STATUS_PUBLISHED || r.published_at > cur.published_at))
                    || (cur.status != STATUS_PUBLISHED
                        && r.status != STATUS_ARCHIVED
                        && r.created_at > cur.created_at)
            }
        };
        if better {
            latest.insert(&r.domain.name, r);
        }
    }
    order.into_iter().map(|n| latest[n].clone()).collect()
}

/// InForce keeps the latest published version of each stored domain of rs (the built-in ones left out: a type
/// catalogue adds them itself).
pub fn in_force(rs: &[Record]) -> Vec<Domain> {
    latest(rs)
        .into_iter()
        .filter(|r| r.status == STATUS_PUBLISHED && !r.builtin)
        .map(|r| r.domain)
        .collect()
}
