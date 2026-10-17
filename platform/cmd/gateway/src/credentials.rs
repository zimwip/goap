//! The adapter of the credentials Connect client to the gateway's [`Credentials`].
use std::time::Duration;

use goap_connect::{ConnectError, Request};
use goap_ctx::Ctx;
use goap_gateway::{BoxError, Credentials};
use goap_gen::credentials::v1::{self as pb, CredentialsService, CredentialsServiceClient};

/// CredentialsClient adapts the credentials Connect client to [`Credentials`]. The errors are the Connect errors
/// themselves, boxed: the gateway reads their code, an `already_exists` (the subject is registered) making a
/// register answer 409.
#[derive(Clone, Debug)]
pub struct CredentialsClient {
    pub rpc: CredentialsServiceClient,
}

fn boxed(e: ConnectError) -> BoxError {
    Box::new(e)
}

#[goap_connect::async_trait]
impl Credentials for CredentialsClient {
    async fn register(&self, ctx: &Ctx, subject: &str, password: &str) -> Result<(), BoxError> {
        self.rpc
            .register(
                ctx,
                Request::new(pb::RegisterRequest {
                    subject: subject.to_string(),
                    password: password.to_string(),
                }),
            )
            .await
            .map(|_| ())
            .map_err(boxed)
    }

    async fn verify(&self, ctx: &Ctx, subject: &str, password: &str) -> Result<bool, BoxError> {
        let r = self
            .rpc
            .verify(
                ctx,
                Request::new(pb::VerifyRequest {
                    subject: subject.to_string(),
                    password: password.to_string(),
                }),
            )
            .await
            .map_err(boxed)?;
        Ok(r.into_inner().ok)
    }

    async fn start_session(
        &self,
        ctx: &Ctx,
        subject: &str,
        max_age: Duration,
    ) -> Result<String, BoxError> {
        let r = self
            .rpc
            .start_session(
                ctx,
                Request::new(pb::StartSessionRequest {
                    subject: subject.to_string(),
                    max_age_seconds: i64::try_from(max_age.as_secs()).unwrap_or(i64::MAX),
                }),
            )
            .await
            .map_err(boxed)?;
        Ok(r.into_inner().id)
    }

    async fn session_active(&self, ctx: &Ctx, id: &str, subject: &str) -> Result<bool, BoxError> {
        let r = self
            .rpc
            .check_session(
                ctx,
                Request::new(pb::CheckSessionRequest {
                    id: id.to_string(),
                    subject: subject.to_string(),
                }),
            )
            .await
            .map_err(boxed)?;
        Ok(r.into_inner().active)
    }

    async fn end_session(&self, ctx: &Ctx, id: &str) -> Result<(), BoxError> {
        self.rpc
            .end_session(
                ctx,
                Request::new(pb::EndSessionRequest { id: id.to_string() }),
            )
            .await
            .map(|_| ())
            .map_err(boxed)
    }

    async fn end_sessions(&self, ctx: &Ctx, subject: &str) -> Result<(), BoxError> {
        self.rpc
            .end_sessions(
                ctx,
                Request::new(pb::EndSessionsRequest {
                    subject: subject.to_string(),
                }),
            )
            .await
            .map(|_| ())
            .map_err(boxed)
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use goap_connect::{Client, Code, Interceptors, axum::Router};
    use goap_credsvc::{Handler, MemoryStore, Service, client_token};
    use goap_gateway::{Config, mount};

    use super::*;

    async fn serve(app: Router) -> String {
        let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", ln.local_addr().unwrap());
        tokio::spawn(async move { goap_connect::axum::serve(ln, app).await });
        url
    }

    async fn credentials() -> CredentialsClient {
        let svc = Arc::new(Service::new(Arc::new(MemoryStore::new())));
        let url = serve(pb::credentials_service_router(
            Arc::new(Handler::new(svc, "s3cret")),
            Interceptors::none(),
        ))
        .await;
        CredentialsClient {
            rpc: CredentialsServiceClient::new(
                Client::builder(&url)
                    .interceptor(client_token("s3cret"))
                    .build(),
            ),
        }
    }

    /// The adapter carries the calls to the credentials service, and a subject registered twice comes back as an
    /// `already_exists` Connect error.
    #[tokio::test]
    async fn the_client_speaks_to_the_credentials_service() {
        let ctx = Ctx::background();
        let c = credentials().await;
        c.register(&ctx, "alice", "correct horse").await.unwrap();
        assert!(c.verify(&ctx, "alice", "correct horse").await.unwrap());
        assert!(!c.verify(&ctx, "alice", "wrong horse").await.unwrap());
        let e = c
            .register(&ctx, "alice", "correct horse")
            .await
            .expect_err("registered twice");
        assert_eq!(
            goap_connect::code_of(&*e as &(dyn std::error::Error + 'static)),
            Code::AlreadyExists,
            "{e}"
        );
        let id = c
            .start_session(&ctx, "alice", Duration::from_secs(3600))
            .await
            .unwrap();
        assert!(c.session_active(&ctx, &id, "alice").await.unwrap());
        c.end_session(&ctx, &id).await.unwrap();
        assert!(!c.session_active(&ctx, &id, "alice").await.unwrap());
        let id = c
            .start_session(&ctx, "alice", Duration::from_secs(3600))
            .await
            .unwrap();
        c.end_sessions(&ctx, "alice").await.unwrap();
        assert!(!c.session_active(&ctx, &id, "alice").await.unwrap());
    }

    /// A register of a subject already registered answers 409 through the gateway.
    #[tokio::test]
    async fn register_twice_is_a_conflict() {
        let cfg = Config {
            auth_mode: "local".into(),
            jwt_secret: b"a-test-secret-of-at-least-32-bytes!".to_vec(),
            credentials: Some(Arc::new(credentials().await)),
            ..Default::default()
        };
        let url = serve(mount(Router::new(), cfg).unwrap()).await;
        let http = reqwest::Client::new();
        let body = serde_json::json!({"subject": "alice", "password": "correct horse"});
        let first = http
            .post(format!("{url}/auth/register"))
            .json(&body)
            .send()
            .await
            .unwrap();
        assert_eq!(first.status(), 200, "{:?}", first.text().await);
        let second = http
            .post(format!("{url}/auth/register"))
            .json(&body)
            .send()
            .await
            .unwrap();
        assert_eq!(second.status(), 409, "{:?}", second.text().await);
    }
}
