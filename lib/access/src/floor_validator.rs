//! The floor of the administrators (ADR 0020, 0048): no change may leave the graph without one.

use async_trait::async_trait;
use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_domain::{BaselineID, Link, Node};
use goap_graphapi::{Error, ErrorKind, NodeValidator, ValidatedNode, ValidatorQuery};

use crate::access::{NODE_TYPE_USER, ROLE_ADMIN, build_snapshot, user_from_props};
use crate::project::NODE_TYPE_ASSIGNMENT;

/// AdminFloorValidator refuses a change that would leave the graph with no active administrator (ADR 0020's floor
/// policy, enforced by ADR 0048's NodeValidator mechanism): "administrator" is a User holding the platform
/// [`ROLE_ADMIN`] as `Snapshot::enrich` grants it (ADR 0047: an Assignment of the user or of a unit above it);
/// "active" is the user lifecycle's active state (ADR 0048). Wired onto Graph.Validators (goap-dev, graph).
#[derive(Debug, Clone, Copy, Default)]
pub struct AdminFloorValidator;

#[async_trait]
impl NodeValidator for AdminFloorValidator {
    /// Types runs this validator whenever a change touches a User or an Assignment (not derived from `adminOnly`: that
    /// flag says who may write a type, this is the pair of types that decides who administers): either can remove the
    /// last administrator (deactivating the User, or retiring/narrowing their platform Assignment).
    fn types(&self) -> Vec<String> {
        vec![NODE_TYPE_USER.into(), NODE_TYPE_ASSIGNMENT.into()]
    }

    /// Validate counts the active administrators the way `Snapshot::enrich` grants the role, on the state the change
    /// leaves: the snapshot of that state is built and each active user enriched, so a platform Assignment held by a
    /// unit the user belongs to (or one of its ancestors) counts as much as one held by the User node itself. Counting
    /// less would refuse a change that leaves an administrator; counting more, accept one that locks everybody out.
    async fn validate(
        &self,
        ctx: &Ctx,
        q: &dyn ValidatorQuery,
        _impacted: &[ValidatedNode],
    ) -> Result<(), Error> {
        let st = goap_typecat::builtin().structures();
        let org = st.organisation();
        let mut nodes: Vec<Node> = Vec::new();
        let mut links: Vec<Link> = Vec::new();
        for typ in [NODE_TYPE_USER, NODE_TYPE_ASSIGNMENT, org.r#type.as_str()] {
            for n in q.nodes_of_type(ctx, &org.namespace, typ).await? {
                links.extend(q.out_links_of(ctx, &n.r#ref()).await?);
                nodes.push(n);
            }
        }
        let snap = build_snapshot(&st, &BaselineID::default(), &nodes, &links);
        for n in &nodes {
            if n.r#type != NODE_TYPE_USER || n.state != "active" {
                continue;
            }
            if let Ok(u) = user_from_props(&n.properties) {
                let p = Principal {
                    subject: u.subject,
                    ..Principal::default()
                };
                if snap.enrich(&p).roles.iter().any(|r| r == ROLE_ADMIN) {
                    return Ok(()); // at least one active administrator remains
                }
            }
        }
        Err(Error::wrap(
            ErrorKind::Invalid,
            "this change would leave no active administrator",
        ))
    }
}
