//! Port of `demo.go`: loading the demo data on demand.

use std::sync::Arc;

use goap_access as access;
use goap_authz::{Request as AuthzRequest, Resource, check, principal_from, with_principal};
use goap_connect::{ConnectError, Ctx, Request, Response};
use goap_gen::graph::v1 as pb;
use goap_graph::Graph;

use crate::boot::{BoxError, BoxFuture};
use crate::handler::Handler;
use crate::personal::SYSTEM_PRINCIPAL;

/// The demo data of a composition (platform/devseed): whether it wrote anything and the key of the demo project.
pub type DemoFn = Arc<
    dyn for<'a> Fn(&'a Ctx, &'a Graph) -> BoxFuture<'a, Result<(bool, String), BoxError>>
        + Send
        + Sync,
>;

/// Makes a [`DemoFn`] of a function.
pub fn demo_fn<F>(f: F) -> DemoFn
where
    F: for<'a> Fn(&'a Ctx, &'a Graph) -> BoxFuture<'a, Result<(bool, String), BoxError>>
        + Send
        + Sync
        + 'static,
{
    Arc::new(f)
}

impl Handler {
    /// LoadDemoData loads the demo data on demand (the "Load demo data" setting): administrators only, checked against
    /// the floor like the access nodes; the seed then acts as the graph service, for the administrator who asked.
    pub(crate) async fn load_demo_data_rpc(
        &self,
        ctx: &Ctx,
        r: Request<pb::LoadDemoDataRequest>,
    ) -> Result<Response<pb::LoadDemoDataResponse>, ConnectError> {
        let Some(demo) = &self.demo else {
            return Err(ConnectError::unimplemented(
                "this deployment ships no demo data",
            ));
        };
        let ctx = &self.identity.context(ctx, &r.headers);
        let who = principal_from(ctx);
        let gate = self.floor.as_ref().or(self.authz.as_ref());
        check(
            ctx,
            gate.map(|a| a.as_ref()),
            &AuthzRequest {
                subject: who.clone(),
                action: "write".into(),
                resource: Resource {
                    r#type: access::RESOURCE_POLICY.into(),
                    org: who.org.clone(),
                    ..Default::default()
                },
            },
        )
        .await
        .map_err(goap_rpcerr::to_connect)?;
        let as_seed = with_principal(ctx, SYSTEM_PRINCIPAL.clone().for_user(who.end_user()));
        let (loaded, project) = demo(&as_seed, &self.graph)
            .await
            .map_err(goap_rpcerr::to_connect)?;
        Ok(Response::new(pb::LoadDemoDataResponse { loaded, project }))
    }
}
