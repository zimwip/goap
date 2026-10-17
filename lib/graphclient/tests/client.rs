use std::sync::Arc;

use goap_authz::{Principal, with_principal};
use goap_connect::{ConnectError, Ctx, Interceptors, Request, Response, async_trait};
use goap_domain::ChangeID;
use goap_gen::graph::v1 as pb;
use goap_graphapi::Port;
use goap_graphclient::{CLIENT_PRINCIPAL, Client};
use goap_identity::HEADER_SUBJECT;

/// A graph service answering GetChange with a change titled by the subject of the request, and NotFound for "nope".
struct Fake;

#[async_trait]
impl pb::GraphService for Fake {
    async fn get_change(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetChangeRequest>,
    ) -> Result<Response<pb::GetChangeResponse>, ConnectError> {
        if req.msg.id == "nope" {
            return Err(ConnectError::not_found("change nope: not found"));
        }
        Ok(Response::new(pb::GetChangeResponse {
            change: Some(pb::Change {
                id: req.msg.id.clone(),
                title: req.header(HEADER_SUBJECT).to_string(),
                ..Default::default()
            }),
        }))
    }
}

async fn serve() -> String {
    let app = pb::graph_service_router(Arc::new(Fake), Interceptors::none());
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(l, app).await.unwrap() });
    format!("http://{addr}")
}

#[tokio::test]
async fn forwards_the_principal_else_names_the_client() {
    let c = Client::new(&serve().await);
    let ctx = Ctx::background();
    let ch = c.change(&ctx, &ChangeID::from("c1")).await.unwrap();
    assert_eq!(ch.id, "c1");
    assert_eq!(ch.title, CLIENT_PRINCIPAL.subject);

    let ctx = with_principal(
        &ctx,
        Principal {
            subject: "alice".into(),
            ..Default::default()
        },
    );
    let ch = c.change(&ctx, &ChangeID::from("c1")).await.unwrap();
    assert_eq!(ch.title, "alice");
}

#[tokio::test]
async fn maps_the_errors_back() {
    let c = Client::new(&serve().await);
    let err = c
        .change(&Ctx::background(), &ChangeID::from("nope"))
        .await
        .unwrap_err();
    assert!(err.is_not_found(), "{err}");
    // an RPC the service does not implement
    let err = c.structures(&Ctx::background()).await.unwrap_err();
    assert!(!err.is_not_found(), "{err}");
}

// The client serves the directories reading the organisation over RPC (access::Graph, graphsnap::Graph).
#[allow(dead_code)]
fn is_access_graph(c: Client) -> Arc<dyn goap_access::Graph> {
    Arc::new(c)
}
