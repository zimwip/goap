//! Port of `personal.go`: personal changes (ADR 0037), the users seen by the platform (ADR 0039, 0040, 0042) and the
//! interceptors identifying the callers.
//!
//! A change held by the personal unit of a person (USR:<subject>) belongs to that person only: nobody else reads it,
//! writes to it, applies or removes it, and it is never split into sub-changes.

use std::collections::HashSet;
use std::sync::{Arc, LazyLock};

use goap_access as access;
use goap_authz::{Principal, principal_from, with_principal};
use goap_connect::{
    AnyRequest, AnyResponse, ConnectError, Ctx, Interceptor, Next, Request, Response, async_trait,
};
use goap_domain::{Change, ChangeID, Node, STRUCTURE_ORGANISATION};
use goap_gen::graph::v1 as pb;
use goap_graph::{Graph, LinkEdit, NodeEdit, Result};
use goap_identity::Extractor;
use goap_pbconv as pbconv;
use parking_lot::Mutex;

use crate::handler::{Handler, res};
use crate::seed_defaults::{link_to, seed_change, seed_node};

/// OwnerMe, as the owner_org of a new change, designates the personal unit of the caller.
pub const OWNER_ME: &str = "@me";

pub(crate) fn subject_of(ctx: &Ctx) -> String {
    principal_from(ctx).subject
}

pub(crate) fn deny_personal(id: &ChangeID) -> ConnectError {
    ConnectError::not_found(format!("change {id}: not found"))
}

impl Handler {
    /// resolveOwner turns "@me" into the personal unit of the caller, creating their User node on first use, and
    /// refuses the personal unit of another person.
    pub(crate) async fn resolve_owner(
        &self,
        ctx: &Ctx,
        owner: &str,
    ) -> std::result::Result<String, ConnectError> {
        if owner == OWNER_ME {
            let me = subject_of(ctx);
            if me.is_empty() {
                return Err(ConnectError::unauthenticated(
                    "personal changes need an identified caller",
                ));
            }
            ensure_user(ctx, &self.graph, &me)
                .await
                .map_err(goap_rpcerr::to_connect)?;
            return Ok(access::personal_unit(&me));
        }
        if access::is_personal_unit(owner) && access::personal_subject(owner) != subject_of(ctx) {
            return Err(ConnectError::permission_denied(format!(
                "unit {owner} is personal to someone else"
            )));
        }
        Ok(owner.to_string())
    }
}

/// The creations of users in this process, one at a time: the web fires several calls at once right after signing
/// in, and two commits creating the same key must not race.
static ENSURE_MU: LazyLock<tokio::sync::Mutex<()>> = LazyLock::new(|| tokio::sync::Mutex::new(()));

/// EnsureUser makes sure the organisation@User node of a subject exists, creating one when it does not (ADR 0039: a
/// user is created automatically, not by an administrator by hand, so they can be assigned roles and appear in the
/// organisation navigation as soon as they are seen). Its key (access::user_key, "USR:<subject>") is also
/// access::personal_unit's: since organisation@User extends organisation@OrgUnit (ADR 0039), the same node doubles as
/// the personal unit that holds a subject's personal changes (ADR 0037) — one node, not two competing for the same
/// key.
///
/// A new user is linked member_of the unit new users join (new_user_unit: the waiting unit an administrator flagged,
/// else the default organisation, ADR 0042), and granted the admin role, through a platform Assignment (ADR 0046,
/// 0047), when it is the very first User node the namespace has ever held (ADR 0040): a fresh deployment otherwise has
/// no path to a first administrator at all. Creations are serialized within a process; a benign race between two
/// processes' simultaneous first connections can grant admin to more than one subject; it never grants it to none,
/// which is what the floor policy (ADR 0020) relies on. The User node, its member_of, and the first user's admin
/// Assignment are committed together, as one change on main (createUser, ADR 0042).
///
/// The default org is resolved *before* the node is created: the bootstrap (boot) can still be running at startup,
/// and a caller seen in that window must not leave a permanently broken User behind. Failing here (not found: the org
/// doesn't exist yet) leaves nothing created, so the next call for this subject starts clean.
pub async fn ensure_user(ctx: &Ctx, g: &Graph, subject: &str) -> Result<()> {
    // a platform service (system:registry, system:trigger:<key>, ...) is no person: it never becomes a User, so it
    // can never take the first-administrator grant below
    if subject.is_empty()
        || (Principal {
            subject: subject.to_string(),
            ..Default::default()
        })
        .is_system()
    {
        return Ok(());
    }
    let key = access::user_key(subject);
    let exists = async || -> Result<bool> {
        match g
            .node_by_key(ctx, access::NAMESPACE_ORGANISATION, &key)
            .await
        {
            Ok(_) => Ok(true),
            Err(e) if e.is_not_found() => Ok(false),
            Err(e) => Err(e),
        }
    };
    if exists().await? {
        return Ok(());
    }
    let _guard = ENSURE_MU.lock().await;
    let mut last = Ok(());
    for _ in 0..3 {
        if exists().await? {
            return Ok(());
        }
        last = Box::pin(create_user(ctx, g, subject)).await;
        match &last {
            Err(e) if e.is_conflict() => {} // another change moved main meanwhile, or another process created the user: read again
            _ => return last,
        }
    }
    last
}

/// createUser commits the User node of a subject on main, member_of the unit new users join, as a change of its own
/// (ADR 0042: every modification is a change, so a new user is journaled and moves the head of main — the snapshot
/// goap-access reads roles and units from sees them at once).
async fn create_user(ctx: &Ctx, g: &Graph, subject: &str) -> Result<()> {
    let org = new_user_unit(ctx, g).await?;
    let existing = g
        .nodes_of_type(ctx, access::NAMESPACE_ORGANISATION, access::NODE_TYPE_USER)
        .await?;
    let u = access::User {
        subject: subject.to_string(),
        ..Default::default()
    };
    let key = access::user_key(subject);
    let mut user = link_to(
        seed_node(&key, access::NODE_TYPE_USER, u.props()),
        access::LINK_MEMBER_OF,
        org.r#ref(),
    );
    user.rationale = format!("First sign-in of {subject}");
    // activate it in the same commit (ADR 0048's user lifecycle starts a new account "proposed"): a sign-in lands it
    // usable right away, with no separate admin step needed for every single new user.
    user.state = "active".into();
    let mut edits = vec![user];
    if existing.is_empty() {
        // the first user: grant admin through a platform Assignment created in the same commit (to_key resolves to
        // the User node above, ADR 0047)
        let asg = access::Assignment {
            roles: vec![access::ROLE_ADMIN.to_string()],
            description: "First user becomes administrator".into(),
        };
        edits.push(NodeEdit {
            key: access::platform_assignment_key(&key),
            r#type: access::NODE_TYPE_ASSIGNMENT.into(),
            props: asg.props(),
            rationale: "First user becomes administrator".into(),
            links: vec![LinkEdit {
                r#type: access::LINK_ASSIGNS_ORG.into(),
                to_key: key.clone(),
                ..Default::default()
            }],
            ..Default::default()
        });
    }
    // the activating transition (and, for the first user, the admin grant) is internal bookkeeping of the sign-in
    // flow, not an action the caller is themselves authorized for: it runs as the system principal (ADR 0048).
    seed_change(
        &system(ctx),
        g,
        access::NAMESPACE_ORGANISATION,
        &format!("User {subject}"),
        edits,
    )
    .await
}

/// NewUserUnit returns the organisational unit new users join (ADR 0042): the waiting unit, an OrgUnit an
/// administrator created and flagged `waiting` (access::PROP_WAITING_UNIT) at their discretion, or the root of the
/// organisation structure when none is flagged. Should several units carry the flag, the smallest key wins. Read
/// against live state, like the rest of ensure_user: a flag moved by a change is seen at once.
pub async fn new_user_unit(ctx: &Ctx, g: &Graph) -> Result<Node> {
    let units = g
        .nodes_of_type(
            ctx,
            access::NAMESPACE_ORGANISATION,
            access::NODE_TYPE_ORG_UNIT,
        )
        .await?;
    let found = units
        .into_iter()
        .filter(|u| access::is_waiting_unit(&u.properties))
        .min_by(|a, b| a.key.cmp(&b.key));
    if let Some(n) = found {
        return Ok(n);
    }
    g.node_by_key(
        ctx,
        access::NAMESPACE_ORGANISATION,
        &g.structure(STRUCTURE_ORGANISATION).root,
    )
    .await
}

/// SystemPrincipal is the identity of the graph service acting by itself (seeds, the bookkeeping of a sign-in): a
/// named service, never an anonymous caller, which the transition authorizers refuse.
pub static SYSTEM_PRINCIPAL: LazyLock<Principal> =
    LazyLock::new(|| goap_authz::system("graph", &[access::ROLE_ADMIN]));

/// System returns ctx acting as the graph service itself.
pub fn system(ctx: &Ctx) -> Ctx {
    with_principal(ctx, SYSTEM_PRINCIPAL.clone())
}

/// Identify is the interceptor that identifies the caller of every RPC (Handler::identify): it applies the identity of
/// the request to the context the handler receives and refuses an anonymous request, so no call reaches the graph
/// without a principal. First of the chain.
pub struct Identify {
    identity: Extractor,
}

#[async_trait]
impl Interceptor for Identify {
    async fn unary(
        &self,
        ctx: Ctx,
        req: AnyRequest,
        next: Next<'_>,
    ) -> std::result::Result<AnyResponse, ConnectError> {
        let who = self.identity.context(&ctx, &req.headers);
        if principal_from(&who).anonymous() {
            return Err(ConnectError::unauthenticated(
                "the caller is not identified",
            ));
        }
        next.run(who, req).await
    }
}

/// EnsureCaller is the interceptor that calls ensure_user for every authenticated caller (ADR 0039), deduplicated per
/// process: a subject already seen is not checked again.
pub struct EnsureCaller {
    identity: Extractor,
    graph: Arc<Graph>,
    seen: Mutex<HashSet<String>>,
}

#[async_trait]
impl Interceptor for EnsureCaller {
    async fn unary(
        &self,
        ctx: Ctx,
        req: AnyRequest,
        next: Next<'_>,
    ) -> std::result::Result<AnyResponse, ConnectError> {
        let who = self.identity.context(&ctx, &req.headers);
        let subject = subject_of(&who);
        if !subject.is_empty()
            && !self.seen.lock().contains(&subject)
            && ensure_user(&who, &self.graph, &subject).await.is_ok()
        {
            self.seen.lock().insert(subject);
        }
        next.run(who, req).await
    }
}

/// PersonalScope is the interceptor that keeps personal changes to their owner: a request naming a change
/// (change_id, or id for GetChange and UpdateChange) that is personal to someone else is answered as not found.
pub struct PersonalScope {
    identity: Extractor,
    graph: Arc<Graph>,
}

#[async_trait]
impl Interceptor for PersonalScope {
    async fn unary(
        &self,
        ctx: Ctx,
        req: AnyRequest,
        next: Next<'_>,
    ) -> std::result::Result<AnyResponse, ConnectError> {
        let who = self.identity.context(&ctx, &req.headers);
        let id = change_id_in(&req.spec.procedure, &req.message_json());
        if !id.is_empty()
            && let Ok(c) = self.graph.change(&who, &id).await
            && access::is_personal(&c)
            && !access::is_personal_to(&c, &subject_of(&who))
        {
            return Err(deny_personal(&id));
        }
        next.run(who, req).await
    }
}

/// changeIDIn returns the change a request names (its protobuf JSON form: changeId, or id for GetChange and
/// UpdateChange).
fn change_id_in(procedure: &str, msg: &serde_json::Value) -> ChangeID {
    let name = if procedure.ends_with("/GetChange") || procedure.ends_with("/UpdateChange") {
        "id"
    } else {
        "changeId"
    };
    msg.get(name)
        .and_then(serde_json::Value::as_str)
        .map(ChangeID::from)
        .unwrap_or_default()
}

/// visibleTo reports whether the caller may see a change: every change but the personal ones of others.
pub(crate) fn visible_to(ctx: &Ctx, c: &Change) -> bool {
    !access::is_personal(c) || access::is_personal_to(c, &subject_of(ctx))
}

impl Handler {
    /// Identify is the interceptor identifying the caller of every RPC ([`Identify`]).
    pub fn identify(&self) -> Identify {
        Identify {
            identity: self.identity.clone(),
        }
    }

    /// EnsureCaller is the interceptor declaring every authenticated caller as a user ([`EnsureCaller`]).
    pub fn ensure_caller(&self) -> EnsureCaller {
        EnsureCaller {
            identity: self.identity.clone(),
            graph: self.graph.clone(),
            seen: Mutex::new(HashSet::new()),
        }
    }

    /// PersonalScope is the interceptor keeping personal changes to their owner ([`PersonalScope`]).
    pub fn personal_scope(&self) -> PersonalScope {
        PersonalScope {
            identity: self.identity.clone(),
            graph: self.graph.clone(),
        }
    }

    pub(crate) async fn delete_change_rpc(
        &self,
        ctx: &Ctx,
        r: Request<pb::DeleteChangeRequest>,
    ) -> std::result::Result<Response<pb::DeleteChangeResponse>, ConnectError> {
        let ctx = &self.identity.context(ctx, &r.headers);
        let out = self
            .graph
            .purge_change(ctx, &ChangeID::from(r.msg.change_id.as_str()))
            .await;
        let out = out.map(|mut c| {
            c.items.clear();
            c.nodes.clear();
            c
        });
        if let Ok(c) = &out {
            self.publish_change(
                ctx,
                &format!("goap.change.{}.purged", c.id),
                goap_domain::ChangeEvent {
                    r#type: "change.purged".into(),
                    change: c.clone(),
                    ..Default::default()
                },
            )
            .await;
        }
        res(out.map(|c| pb::DeleteChangeResponse {
            change: Some(pbconv::change_to_pb(&c)),
        }))
    }
}
