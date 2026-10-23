//! The smart tags (`graph/smarttagsvc.SQLStore`, ADR 0143 §7): named selections of exact node versions, shared with
//! users, in tables of their own in the database of the graph. The times are `timestamptz` on PostgreSQL and the
//! RFC 3339 text Go writes on SQLite.
use chrono::{DateTime, Utc};
use graph_core::{GraphError, gojson};

use super::{
    Session,
    value::{Arg, Val},
};

/// A smart tag as stored.
#[derive(Debug, Clone, Default)]
pub struct SmartTag {
    pub id: String,
    pub name: String,
    pub description: String,
    pub owner: String,
    /// The versions it selects (node id, version), by node then version.
    pub versions: Vec<(String, i32)>,
    /// Who it is shared with (subject, access), by subject.
    pub shares: Vec<(String, String)>,
    pub by: String,
    pub created_at: Option<DateTime<Utc>>,
    pub updated_at: Option<DateTime<Utc>>,
}

/// Which smart tags to read.
pub enum Which<'a> {
    /// One, by id; locked for update on PostgreSQL when `lock`.
    Id { id: &'a str, lock: bool },
    /// Those a subject owns or that are shared with them, selecting a version of a node when one is named.
    Visible { subject: &'a str, node: &'a str },
}

fn time_of(v: &Val) -> Option<DateTime<Utc>> {
    match v {
        Val::Time(t) => Some(*t),
        Val::Text(s) => DateTime::parse_from_rfc3339(s).ok().map(|t| t.with_timezone(&Utc)),
        _ => None,
    }
}

impl Session {
    fn time_arg(&self, t: &DateTime<Utc>) -> Arg {
        if self.sql.pg {
            Arg::Time(*t)
        } else {
            Arg::Text(gojson::time_text(t))
        }
    }

    /// The smart tags asked, with their versions and shares (`SQLStore.read`).
    pub async fn smart_tags(&self, which: Which<'_>) -> Result<Vec<SmartTag>, GraphError> {
        let ph = |i| self.sql.ph(i);
        let (filter, args): (String, Vec<Arg>) = match which {
            Which::Id { id, lock } => (
                format!("id = {}{}", ph(1), if lock && self.sql.pg { " FOR UPDATE" } else { "" }),
                vec![id.into()],
            ),
            Which::Visible { subject, node } => {
                let mut w = format!(
                    "(owner = {} OR id IN (SELECT tag_id FROM smart_tag_share WHERE subject = {}))",
                    ph(1),
                    ph(2)
                );
                let mut args: Vec<Arg> = vec![subject.into(), subject.into()];
                if !node.is_empty() {
                    w.push_str(&format!(
                        " AND id IN (SELECT tag_id FROM smart_tag_version WHERE node_id = {})",
                        ph(3)
                    ));
                    args.push(node.into());
                }
                w.push_str(" ORDER BY name, id");
                (w, args)
            }
        };
        let rows = self
            .select(
                (
                    format!(
                        "SELECT id, name, description, owner, by, created_at, updated_at FROM smart_tag WHERE {filter}"
                    ),
                    args,
                ),
                "smart tag",
            )
            .await?;
        let mut out: Vec<SmartTag> = rows
            .iter()
            .map(|r| SmartTag {
                id: r.text(0),
                name: r.text(1),
                description: r.text(2),
                owner: r.text(3),
                by: r.text(4),
                created_at: time_of(&r.0[5]),
                updated_at: time_of(&r.0[6]),
                ..SmartTag::default()
            })
            .collect();
        if out.is_empty() {
            return Ok(out);
        }
        let ids: Vec<Arg> = out.iter().map(|t| t.id.as_str().into()).collect();
        let within = (1..=ids.len()).map(ph).collect::<Vec<_>>().join(", ");
        let at = |id: &str, out: &[SmartTag]| out.iter().position(|t| t.id == id);
        let vs = self
            .select(
                (
                    format!(
                        "SELECT tag_id, node_id, version FROM smart_tag_version WHERE tag_id IN ({within}) \
                         ORDER BY tag_id, node_id, version"
                    ),
                    ids.clone(),
                ),
                "smart tag versions",
            )
            .await?;
        for r in &vs {
            if let Some(i) = at(&r.text(0), &out) {
                out[i].versions.push((r.text(1), r.int32(2)));
            }
        }
        let ss = self
            .select(
                (
                    format!(
                        "SELECT tag_id, subject, access FROM smart_tag_share WHERE tag_id IN ({within}) \
                         ORDER BY tag_id, subject"
                    ),
                    ids,
                ),
                "smart tag shares",
            )
            .await?;
        for r in &ss {
            if let Some(i) = at(&r.text(0), &out) {
                out[i].shares.push((r.text(1), r.text(2)));
            }
        }
        Ok(out)
    }

    /// Writes the selection and the shares of a smart tag, replacing those it had.
    async fn write_smart_tag_parts(&self, t: &SmartTag) -> Result<(), GraphError> {
        self.delete_smart_tag_parts(&t.id).await?;
        let ph = |i| self.sql.ph(i);
        for (node, version) in &t.versions {
            let q = format!(
                "INSERT INTO smart_tag_version (tag_id, node_id, version) VALUES ({}, {}, {})",
                ph(1),
                ph(2),
                ph(3)
            );
            self.run(
                (
                    q,
                    vec![t.id.as_str().into(), node.as_str().into(), Arg::Int((*version).into())],
                ),
                "smart tag",
            )
            .await?;
        }
        for (subject, access) in &t.shares {
            let q = format!(
                "INSERT INTO smart_tag_share (tag_id, subject, access) VALUES ({}, {}, {})",
                ph(1),
                ph(2),
                ph(3)
            );
            self.run(
                (
                    q,
                    vec![t.id.as_str().into(), subject.as_str().into(), access.as_str().into()],
                ),
                "smart tag",
            )
            .await?;
        }
        Ok(())
    }

    async fn delete_smart_tag_parts(&self, id: &str) -> Result<(), GraphError> {
        for table in ["smart_tag_version", "smart_tag_share"] {
            let q = format!("DELETE FROM {table} WHERE tag_id = {}", self.sql.ph(1));
            self.run((q, vec![id.into()]), "smart tag").await?;
        }
        Ok(())
    }

    /// Stores a new smart tag.
    pub async fn create_smart_tag(&self, t: &SmartTag) -> Result<(), GraphError> {
        let now = Utc::now();
        let q = format!(
            "INSERT INTO smart_tag (id, name, description, owner, by, created_at, updated_at) VALUES ({})",
            (1..=7).map(|i| self.sql.ph(i)).collect::<Vec<_>>().join(", ")
        );
        let args = vec![
            t.id.as_str().into(),
            t.name.as_str().into(),
            t.description.as_str().into(),
            t.owner.as_str().into(),
            t.by.as_str().into(),
            self.time_arg(&t.created_at.unwrap_or(now)),
            self.time_arg(&t.updated_at.unwrap_or(now)),
        ];
        self.run((q, args), "smart tag").await?;
        self.write_smart_tag_parts(t).await
    }

    /// Stores what a smart tag became (its header, selection and shares).
    pub async fn update_smart_tag(&self, t: &SmartTag) -> Result<(), GraphError> {
        let ph = |i| self.sql.ph(i);
        let q = format!(
            "UPDATE smart_tag SET name = {}, description = {}, by = {}, updated_at = {} WHERE id = {}",
            ph(1),
            ph(2),
            ph(3),
            ph(4),
            ph(5)
        );
        let args = vec![
            t.name.as_str().into(),
            t.description.as_str().into(),
            t.by.as_str().into(),
            self.time_arg(&t.updated_at.unwrap_or_else(Utc::now)),
            t.id.as_str().into(),
        ];
        self.run((q, args), "smart tag").await?;
        self.write_smart_tag_parts(t).await
    }

    /// Removes a smart tag with its selection and shares; false when there was none.
    pub async fn delete_smart_tag(&self, id: &str) -> Result<bool, GraphError> {
        self.delete_smart_tag_parts(id).await?;
        let q = format!("DELETE FROM smart_tag WHERE id = {}", self.sql.ph(1));
        Ok(self.run((q, vec![id.into()]), "smart tag").await? > 0)
    }
}
