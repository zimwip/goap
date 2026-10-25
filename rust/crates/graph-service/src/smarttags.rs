//! `goap.smarttag.v1.SmartTagService` (Go's `graph/smarttagsvc`, ADR 0143 §7): the smart tags of the callers, named
//! selections of exact node versions their owner shares with users, to read or to edit. A smart tag is data of its own:
//! no change, no journal, no event; every request acts for its caller, a person or a service.
use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_proto::smarttag::v1 as pb;
use graph_core::{
    GraphError,
    model::{NodeId, NodeRef},
    store::ReadTx,
};
use graph_store::read::{Session, SmartTag, Which};

use crate::{SmartTags, convert, graph::connect_error};

type Answer<T> = Result<Response<T>, ConnectError>;

const HEADER_SUBJECT: &str = "x-goap-subject";

const MAX_NAME_RUNES: usize = 200;
const MAX_DESCRIPTION_RUNES: usize = 4000;
const MAX_VERSIONS: usize = 1000;
const MAX_SHARES: usize = 200;

const ACCESS_READ: &str = "read";
const ACCESS_EDIT: &str = "edit";
const ACCESS_OWNER: &str = "owner";

/// The errors of the service, worded as Go words them.
fn invalid(why: impl std::fmt::Display) -> ConnectError {
    ConnectError::invalid_argument(format!("invalid: {why}"))
}

fn not_found(id: &str) -> ConnectError {
    ConnectError::not_found(format!("smart tag {id}: not found"))
}

fn forbidden(why: impl std::fmt::Display) -> ConnectError {
    ConnectError::permission_denied(format!("{why}: permission denied"))
}

fn internal(e: impl std::fmt::Display) -> ConnectError {
    connect_error(GraphError::internal(e.to_string()))
}

/// The caller: a smart tag belongs to a user.
fn caller<T>(req: &Request<T>) -> Result<String, ConnectError> {
    let s = req.header(HEADER_SUBJECT).to_string();
    if s.is_empty() {
        return Err(ConnectError::unauthenticated("smart tags need an identified user"));
    }
    Ok(s)
}

fn access_of(t: &SmartTag, subject: &str) -> String {
    if subject == t.owner {
        return ACCESS_OWNER.into();
    }
    t.shares
        .iter()
        .find(|(s, _)| s == subject)
        .map(|(_, a)| a.clone())
        .unwrap_or_default()
}

fn check_name(name: &str) -> Result<String, ConnectError> {
    let name = name.trim();
    if name.is_empty() {
        return Err(invalid("a smart tag needs a name"));
    }
    if name.chars().count() > MAX_NAME_RUNES {
        return Err(invalid(format!("a name is at most {MAX_NAME_RUNES} characters")));
    }
    Ok(name.to_string())
}

fn check_description(d: &str) -> Result<(), ConnectError> {
    if d.chars().count() > MAX_DESCRIPTION_RUNES {
        return Err(invalid(format!(
            "a description is at most {MAX_DESCRIPTION_RUNES} characters"
        )));
    }
    Ok(())
}

fn refs(vs: &[pb::TaggedVersion]) -> Vec<(String, i32)> {
    vs.iter().map(|v| (v.node_id.clone(), v.version)).collect()
}

fn check_refs(rs: &[(String, i32)]) -> Result<(), ConnectError> {
    match rs.iter().find(|(id, v)| id.is_empty() || *v <= 0) {
        Some((id, v)) => Err(invalid(format!("a smart tag selects written versions, not {id}@v{v}"))),
        None => Ok(()),
    }
}

/// The versions without duplicates, by node then version (Go's `sorted`, bytes order).
fn sorted(mut rs: Vec<(String, i32)>) -> Vec<(String, i32)> {
    rs.sort();
    rs.dedup();
    rs
}

/// What a subject sees of a smart tag: its owner every share, the others their own.
fn to_pb(mut t: SmartTag, subject: &str) -> pb::SmartTag {
    let access = access_of(&t, subject);
    if subject != t.owner {
        t.shares.retain(|(s, _)| s == subject);
    }
    pb::SmartTag {
        id: t.id,
        name: t.name,
        description: t.description,
        owner: t.owner,
        versions: t
            .versions
            .into_iter()
            .map(|(node_id, version)| pb::TaggedVersion { node_id, version })
            .collect(),
        shares: t
            .shares
            .into_iter()
            .map(|(subject, access)| pb::Share { subject, access })
            .collect(),
        access,
        by: t.by,
        created_at: convert::time(&t.created_at),
        updated_at: convert::time(&t.updated_at),
    }
}

impl SmartTags {
    async fn reader(&self) -> Result<Session, ConnectError> {
        self.state.store.reader().await.map_err(internal)
    }

    async fn writer(&self) -> Result<Session, ConnectError> {
        self.state.store.writer().await.map_err(internal)
    }

    /// Refuses versions that do not exist (`VersionsExist` over the graph's `Node`: a version 0 is the latest on
    /// main). A node id that is no UUID PostgreSQL refuses, in its words, as Go's store passes them on.
    async fn exists(&self, tx: &Session, rs: &[(String, i32)]) -> Result<(), ConnectError> {
        for (id, v) in rs {
            let what = if *v == 0 {
                format!("node {id} on main")
            } else {
                format!("node {id}@v{v}")
            };
            let refused = |why: &str| invalid(format!("version {id}@v{v}: {what}: {why}"));
            if uuid::Uuid::parse_str(id).is_err() {
                if self.state.store.dialect() == graph_store::Dialect::Postgres {
                    return Err(refused(&format!(
                        "invalid input syntax for type uuid: \"{id}\": invalid"
                    )));
                }
                return Err(refused("not found"));
            }
            let node = if *v == 0 {
                tx.latest_on(&NodeId(id.clone()), "main").await
            } else {
                tx.node(&NodeRef::new(NodeId(id.clone()), *v)).await
            };
            if node.map_err(connect_error)?.is_none() {
                return Err(refused("not found"));
            }
        }
        Ok(())
    }

    /// Applies a change to a smart tag the caller has at least `want` access to (`Service.change`), in one transaction.
    async fn change<F>(&self, who: &str, id: &str, want: &str, f: F) -> Answer<pb::SmartTag>
    where
        F: FnOnce(&mut SmartTag) -> Result<(), ConnectError>,
    {
        let tx = self.writer().await?;
        let mut t = tx
            .smart_tags(Which::Id { id, lock: true })
            .await
            .map_err(connect_error)?
            .into_iter()
            .next()
            .ok_or_else(|| not_found(id))?;
        let have = access_of(&t, who);
        if have.is_empty() {
            return Err(not_found(id));
        }
        if (want == ACCESS_OWNER && have != ACCESS_OWNER) || (want == ACCESS_EDIT && have == ACCESS_READ) {
            return Err(forbidden(format!("smart tag {id}: {want} access needed")));
        }
        f(&mut t)?;
        t.by = who.to_string();
        t.updated_at = Some(chrono::Utc::now());
        tx.update_smart_tag(&t).await.map_err(connect_error)?;
        tx.commit().await.map_err(internal)?;
        Ok(Response::new(to_pb(t, who)))
    }
}

#[async_trait]
impl pb::SmartTagService for SmartTags {
    async fn list_smart_tags(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListSmartTagsRequest>,
    ) -> Answer<pb::ListSmartTagsResponse> {
        let who = caller(&req)?;
        let node = req.into_inner().node_id;
        let tx = self.reader().await?;
        let mut ts = tx
            .smart_tags(Which::Visible {
                subject: &who,
                node: &node,
            })
            .await
            .map_err(connect_error)?;
        ts.sort_by(|a, b| a.name.cmp(&b.name).then_with(|| a.id.cmp(&b.id)));
        Ok(Response::new(pb::ListSmartTagsResponse {
            smart_tags: ts.into_iter().map(|t| to_pb(t, &who)).collect(),
        }))
    }

    async fn get_smart_tag(&self, _ctx: &Ctx, req: Request<pb::GetSmartTagRequest>) -> Answer<pb::GetSmartTagResponse> {
        let who = caller(&req)?;
        let id = req.into_inner().id;
        let tx = self.reader().await?;
        let t = tx
            .smart_tags(Which::Id { id: &id, lock: false })
            .await
            .map_err(connect_error)?
            .into_iter()
            .next()
            .filter(|t| !access_of(t, &who).is_empty())
            .ok_or_else(|| not_found(&id))?;
        Ok(Response::new(pb::GetSmartTagResponse {
            smart_tag: Some(to_pb(t, &who)),
        }))
    }

    async fn create_smart_tag(
        &self,
        _ctx: &Ctx,
        req: Request<pb::CreateSmartTagRequest>,
    ) -> Answer<pb::CreateSmartTagResponse> {
        let who = caller(&req)?;
        let r = req.into_inner();
        let name = check_name(&r.name)?;
        check_description(&r.description)?;
        let versions = refs(&r.versions);
        check_refs(&versions)?;
        let versions = sorted(versions);
        if versions.len() > MAX_VERSIONS {
            return Err(invalid(format!("a smart tag selects at most {MAX_VERSIONS} versions")));
        }
        let tx = self.writer().await?;
        self.exists(&tx, &versions).await?;
        let now = chrono::Utc::now();
        let t = SmartTag {
            id: uuid::Uuid::new_v4().simple().to_string(),
            name,
            description: r.description,
            owner: who.clone(),
            versions,
            shares: Vec::new(),
            by: who.clone(),
            created_at: Some(now),
            updated_at: Some(now),
        };
        tx.create_smart_tag(&t).await.map_err(connect_error)?;
        tx.commit().await.map_err(internal)?;
        Ok(Response::new(pb::CreateSmartTagResponse {
            smart_tag: Some(to_pb(t, &who)),
        }))
    }

    async fn update_smart_tag(
        &self,
        _ctx: &Ctx,
        req: Request<pb::UpdateSmartTagRequest>,
    ) -> Answer<pb::UpdateSmartTagResponse> {
        let who = caller(&req)?;
        let r = req.into_inner();
        let (add, remove) = (refs(&r.add), refs(&r.remove));
        check_refs(&add)?;
        if r.set_description {
            check_description(&r.description)?;
        }
        let name = if r.name.is_empty() {
            String::new()
        } else {
            check_name(&r.name)?
        };
        if !add.is_empty() {
            self.exists(&self.reader().await?, &add).await?;
        }
        let res = self
            .change(&who, &r.id, ACCESS_EDIT, |t| {
                if !name.is_empty() {
                    t.name = name;
                }
                if r.set_description {
                    t.description = r.description;
                }
                let mut vs = std::mem::take(&mut t.versions);
                vs.extend(add);
                vs.retain(|v| !remove.contains(v));
                let vs = sorted(vs);
                if vs.len() > MAX_VERSIONS {
                    return Err(invalid(format!("a smart tag selects at most {MAX_VERSIONS} versions")));
                }
                t.versions = vs;
                Ok(())
            })
            .await?;
        Ok(Response::new(pb::UpdateSmartTagResponse {
            smart_tag: Some(res.into_inner()),
        }))
    }

    async fn share_smart_tag(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ShareSmartTagRequest>,
    ) -> Answer<pb::ShareSmartTagResponse> {
        let who = caller(&req)?;
        let r = req.into_inner();
        let subject = r.subject.trim().to_string();
        if subject.is_empty() {
            return Err(invalid("a share names a user"));
        }
        if !r.access.is_empty() && r.access != ACCESS_READ && r.access != ACCESS_EDIT {
            return Err(invalid(format!(
                "access is {ACCESS_READ:?} or {ACCESS_EDIT:?}, not {:?}",
                r.access
            )));
        }
        let res = self
            .change(&who, &r.id, ACCESS_OWNER, |t| {
                if subject == who {
                    return Err(invalid("the owner of a smart tag needs no share"));
                }
                t.shares.retain(|(s, _)| *s != subject);
                if !r.access.is_empty() {
                    if t.shares.len() >= MAX_SHARES {
                        return Err(invalid(format!(
                            "a smart tag is shared with at most {MAX_SHARES} users"
                        )));
                    }
                    t.shares.push((subject.clone(), r.access.clone()));
                    t.shares.sort_by(|a, b| a.0.cmp(&b.0));
                }
                Ok(())
            })
            .await?;
        Ok(Response::new(pb::ShareSmartTagResponse {
            smart_tag: Some(res.into_inner()),
        }))
    }

    async fn delete_smart_tag(
        &self,
        _ctx: &Ctx,
        req: Request<pb::DeleteSmartTagRequest>,
    ) -> Answer<pb::DeleteSmartTagResponse> {
        let who = caller(&req)?;
        let id = req.into_inner().id;
        let tx = self.writer().await?;
        let t = tx
            .smart_tags(Which::Id { id: &id, lock: false })
            .await
            .map_err(connect_error)?
            .into_iter()
            .next()
            .ok_or_else(|| not_found(&id))?;
        match access_of(&t, &who).as_str() {
            "" => return Err(not_found(&id)),
            ACCESS_OWNER => {}
            _ => return Err(forbidden(format!("smart tag {id}: only its owner deletes it"))),
        }
        if !tx.delete_smart_tag(&id).await.map_err(connect_error)? {
            return Err(not_found(&id));
        }
        tx.commit().await.map_err(internal)?;
        Ok(Response::new(pb::DeleteSmartTagResponse {}))
    }
}
