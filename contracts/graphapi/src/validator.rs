//! Graph-wide checks of the state a change leaves (ADR 0048).

use goap_ctx::Ctx;
use goap_domain::{Link, Node, NodeRef};

use crate::Error;

/// NodeValidator checks a graph-wide invariant the resulting state of a change must uphold (ADR 0048). Unlike a
/// property validator (ADR 0018: pure, single-node, no graph access, run by validateProps), a NodeValidator may query
/// the rest of the graph as it will be once the change lands. It runs once per Apply, after every node write of the
/// change is checked (checkChangeImpacts) and before the resulting baseline is persisted: an error here aborts the
/// whole Apply, so the change never lands.
#[async_trait::async_trait]
pub trait NodeValidator: Send + Sync {
    /// Types names the node types whose creation, modification or retirement in a change should trigger this
    /// validator: validate is only called when the change's own impact list touches at least one of them.
    fn types(&self) -> Vec<String>;
    /// Validate checks the invariant. impacted is the subset of the change's own writes whose type is one of
    /// types(); q gives read access to the rest of the graph as it will be once the change lands.
    async fn validate(
        &self,
        ctx: &Ctx,
        q: &dyn ValidatorQuery,
        impacted: &[ValidatedNode],
    ) -> Result<(), Error>;
}

/// ValidatedNode is one node version a change wrote, of a type a NodeValidator declared interest in.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ValidatedNode {
    /// The version the change produces.
    pub post: Node,
    /// None for a created node.
    pub pre: Option<Node>,
}

/// ValidatorQuery is the read access into the resulting state of the change being applied that a NodeValidator gets:
/// bound to the transaction Apply already runs in (there is no nested transaction, the stores are not reentrant) and
/// to the exact node versions the resulting baseline will hold.
#[async_trait::async_trait]
pub trait ValidatorQuery: Send + Sync {
    /// NodesOfType returns the live, non-deleted nodes of a type in a namespace, as the change leaves them.
    async fn nodes_of_type(
        &self,
        ctx: &Ctx,
        namespace: &str,
        typ: &str,
    ) -> Result<Vec<Node>, Error>;
    /// OutLinksOf returns the outgoing links of a node version as the change leaves it.
    async fn out_links_of(&self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>, Error>;
    /// Node resolves a node: an exact version if r.version is set, else the version the change leaves it at.
    async fn node(&self, ctx: &Ctx, r: &NodeRef) -> Result<Node, Error>;
}

const _: Option<&dyn NodeValidator> = None;
