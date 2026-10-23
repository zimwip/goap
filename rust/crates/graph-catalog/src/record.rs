//! The stored versions of the domains (def.Record) and which of them are in force (def.Latest, def.InForce).
use chrono::{DateTime, Utc};

use crate::def::Domain;

pub const STATUS_DRAFT: &str = "draft";
pub const STATUS_PUBLISHED: &str = "published";
pub const STATUS_ARCHIVED: &str = "archived";

/// A stored domain version.
#[derive(Debug, Clone)]
pub struct Record {
    pub domain: Domain,
    pub status: String,
    pub created_at: Option<DateTime<Utc>>,
    pub updated_at: Option<DateTime<Utc>>,
    pub published_at: Option<DateTime<Utc>>,
    pub updated_by: String,
}

/// A time of the registry's rows (RFC 3339, UTC; empty: none).
pub fn parse_time(s: &str) -> Result<Option<DateTime<Utc>>, String> {
    if s.is_empty() {
        return Ok(None);
    }
    DateTime::parse_from_rfc3339(s)
        .map(|t| Some(t.with_timezone(&Utc)))
        .map_err(|e| format!("parsing time {s:?}: {e}"))
}

fn after(a: Option<DateTime<Utc>>, b: Option<DateTime<Utc>>) -> bool {
    a.unwrap_or(DateTime::<Utc>::MIN_UTC) > b.unwrap_or(DateTime::<Utc>::MIN_UTC)
}

/// The latest version of each domain, in their first order: the latest published one if any, the latest draft
/// otherwise.
pub fn latest(rs: &[Record]) -> Vec<&Record> {
    let mut order: Vec<&str> = Vec::new();
    let mut best: Vec<&Record> = Vec::new();
    for r in rs {
        let name = r.domain.name.as_str();
        match order.iter().position(|n| *n == name) {
            None => {
                order.push(name);
                best.push(r);
            }
            Some(i) => {
                let cur = best[i];
                let better = (r.status == STATUS_PUBLISHED
                    && (cur.status != STATUS_PUBLISHED || after(r.published_at, cur.published_at)))
                    || (cur.status != STATUS_PUBLISHED
                        && r.status != STATUS_ARCHIVED
                        && after(r.created_at, cur.created_at));
                if better {
                    best[i] = r;
                }
            }
        }
    }
    best
}

/// The latest published version of each stored domain.
pub fn in_force(rs: &[Record]) -> Vec<Domain> {
    latest(rs)
        .into_iter()
        .filter(|r| r.status == STATUS_PUBLISHED)
        .map(|r| r.domain.clone())
        .collect()
}
