//! The generated server traits, routers and clients, end to end over goap-connect.
use std::sync::Arc;

use futures::StreamExt;
use goap_connect::{
    Client, Code, ConnectError, Ctx, Interceptors, Request, Response, ServerStream, async_trait,
};
use goap_gen::events::v1::{self as ev, EventService, EventServiceClient};
use goap_gen::preferences::v1::{self as pb, PreferencesService, PreferencesServiceClient};

struct Prefs;

#[async_trait]
impl PreferencesService for Prefs {
    async fn get_preferences(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetPreferencesRequest>,
    ) -> Result<Response<pb::GetPreferencesResponse>, ConnectError> {
        let who = req.header("x-goap-subject").to_string();
        let mut fields = std::collections::HashMap::new();
        fields.insert(
            "who".to_string(),
            pbjson_types::Value {
                kind: Some(pbjson_types::value::Kind::StringValue(who)),
            },
        );
        Ok(Response::new(pb::GetPreferencesResponse {
            values: Some(pbjson_types::Struct { fields }),
        }))
    }
}

struct Events;

#[async_trait]
impl EventService for Events {
    async fn watch(
        &self,
        _ctx: &Ctx,
        req: Request<ev::WatchRequest>,
    ) -> Result<Response<ServerStream<ev::Event>>, ConnectError> {
        let from = req.msg.after_seq;
        let s = futures::stream::iter((from + 1..from + 4).map(|seq| {
            Ok(ev::Event {
                epoch: "e1".into(),
                seq,
                ..Default::default()
            })
        }));
        Ok(Response::new(Box::pin(s)))
    }
}

async fn serve(app: goap_connect::axum::Router) -> String {
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = ln.local_addr().unwrap();
    tokio::spawn(async move { goap_connect::axum::serve(ln, app).await.unwrap() });
    format!("http://{addr}")
}

#[test]
fn every_service_has_its_table() {
    assert_eq!(
        pb::preferences_service_methods::SERVICE,
        "goap.preferences.v1.PreferencesService"
    );
    assert_eq!(
        goap_gen::graph::v1::graph_service_methods::METHODS
            .iter()
            .filter(|m| m.2)
            .count(),
        1
    );
}

#[tokio::test]
async fn served_and_called() {
    let app = pb::preferences_service_router(Arc::new(Prefs), Interceptors::none()).merge(
        ev::event_service_router(Arc::new(Events), Interceptors::none()),
    );
    let base = serve(app).await;
    let ctx = Ctx::background();

    let client = PreferencesServiceClient::new(
        Client::builder(&base)
            .header("x-goap-subject", "alice")
            .build(),
    );
    let got = client
        .get_preferences(&ctx, Request::new(pb::GetPreferencesRequest {}))
        .await
        .unwrap();
    let who = &got.msg.values.unwrap().fields["who"];
    assert_eq!(
        who.kind,
        Some(pbjson_types::value::Kind::StringValue("alice".into()))
    );

    // a method the service does not implement answers unimplemented, worded as connect-go's
    let err = client
        .reset_preferences(&ctx, Request::new(pb::ResetPreferencesRequest {}))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::Unimplemented);
    assert_eq!(
        err.message(),
        "goap.preferences.v1.PreferencesService.ResetPreferences is not implemented"
    );

    // the web's JSON: unknown fields are ignored, the answer is protobuf JSON
    let resp = reqwest::Client::new()
        .post(format!(
            "{base}/goap.preferences.v1.PreferencesService/GetPreferences"
        ))
        .header("content-type", "application/json")
        .header("x-goap-subject", "bob")
        .body(r#"{"notAField": true}"#)
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    let v: serde_json::Value = resp.json().await.unwrap();
    assert_eq!(v, serde_json::json!({"values": {"who": "bob"}}));

    // a server stream
    let events = EventServiceClient::new(Client::builder(&base).json().build());
    let r = events
        .watch(
            &ctx,
            Request::new(ev::WatchRequest {
                epoch: String::new(),
                after_seq: 4,
            }),
        )
        .await
        .unwrap();
    let seqs: Vec<u64> = r.msg.map(|e| e.unwrap().seq).collect().await;
    assert_eq!(seqs, vec![5, 6, 7]);
    let err = events
        .heartbeat(&ctx, Request::new(ev::HeartbeatRequest::default()))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::Unimplemented);
}

#[tokio::test]
async fn the_client_is_the_service() {
    // the client implements the trait, so it stands for the service wherever an Arc<dyn Service> is expected
    let base = serve(pb::preferences_service_router(
        Arc::new(Prefs),
        Interceptors::none(),
    ))
    .await;
    let remote: Arc<dyn PreferencesService> = Arc::new(PreferencesServiceClient::connect(&base));
    let proxy = serve(pb::preferences_service_router(remote, Interceptors::none())).await;
    let got = PreferencesServiceClient::connect(&proxy)
        .get_preferences(
            &Ctx::background(),
            Request::new(pb::GetPreferencesRequest {}),
        )
        .await
        .unwrap();
    assert!(got.msg.values.is_some());
}
