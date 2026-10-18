use std::sync::Arc;

use goap_connect::{Client, Code, Ctx, Request};
use goap_credsvc::{Handler, MemoryStore, Service, client_token};
use goap_gen::credentials::v1::{self as pb, CredentialsService};

async fn serve(svc: Arc<Service>, token: &str) -> String {
    let app = pb::credentials_service_router(
        Arc::new(Handler::new(svc, token)),
        goap_connect::Interceptors::none(),
    );
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(ln, app).await });
    url
}

/// Only the gateway, presenting the service credential, may call the credentials service: without it nobody can
/// verify a password, set one or end sessions; and a service with no credential configured refuses everybody.
#[tokio::test]
async fn credentials_need_the_service_token() {
    let ctx = Ctx::background();
    let svc = Arc::new(Service::new(Arc::new(MemoryStore::new())));
    let url = serve(svc.clone(), "s3cret").await;
    let anon = pb::CredentialsServiceClient::connect(&url);
    let gateway = pb::CredentialsServiceClient::new(
        Client::builder(&url)
            .interceptor(client_token("s3cret"))
            .build(),
    );
    let wrong = pb::CredentialsServiceClient::new(
        Client::builder(&url)
            .interceptor(client_token("nope"))
            .build(),
    );

    gateway
        .register(
            &ctx,
            Request::new(pb::RegisterRequest {
                subject: "alice".into(),
                password: "correct horse".into(),
            }),
        )
        .await
        .unwrap();
    for (name, cl) in [("no token", &anon), ("wrong token", &wrong)] {
        let e = cl
            .set_password(
                &ctx,
                Request::new(pb::SetPasswordRequest {
                    subject: "alice".into(),
                    password: "hijacked".into(),
                }),
            )
            .await
            .unwrap_err();
        assert_eq!(e.code(), Code::Unauthenticated, "{name}: SetPassword = {e}");
        let e = cl
            .end_sessions(
                &ctx,
                Request::new(pb::EndSessionsRequest {
                    subject: "alice".into(),
                }),
            )
            .await
            .unwrap_err();
        assert_eq!(e.code(), Code::Unauthenticated, "{name}: EndSessions = {e}");
        let e = cl
            .verify(
                &ctx,
                Request::new(pb::VerifyRequest {
                    subject: "alice".into(),
                    password: "correct horse".into(),
                }),
            )
            .await
            .unwrap_err();
        assert_eq!(e.code(), Code::Unauthenticated, "{name}: Verify = {e}");
    }
    assert!(
        svc.verify(&ctx, "alice", "correct horse").await.unwrap(),
        "the password changed through a refused call"
    );
    let r = gateway
        .verify(
            &ctx,
            Request::new(pb::VerifyRequest {
                subject: "alice".into(),
                password: "correct horse".into(),
            }),
        )
        .await
        .unwrap();
    assert!(r.msg.ok, "gateway verify");

    // no token configured: nobody gets in, not even with an empty header
    let open = serve(svc, "").await;
    let cl = pb::CredentialsServiceClient::connect(&open);
    let e = cl
        .exists(
            &ctx,
            Request::new(pb::ExistsRequest {
                subject: "alice".into(),
            }),
        )
        .await
        .unwrap_err();
    assert_eq!(e.code(), Code::Unauthenticated, "no token configured: {e}");
}
