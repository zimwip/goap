use std::sync::Arc;

use goap_connect::{
    AnyRequest, AnyResponse, Code, ConnectError, Ctx, Interceptor, Next, Request, Response,
    async_trait,
};
use goap_gen::credentials::v1 as pb;
use http::HeaderMap;

use crate::service::{Error, Service};

/// TokenHeader carries the service credential of the gateway on every request to the credentials service.
pub const TOKEN_HEADER: &str = "X-Goap-Credentials-Token";

/// Handler implements the Connect `CredentialsService`. Only the gateway calls it: the subject is a request field,
/// not derived from caller identity (ADR 0040 — there is nothing to authenticate before a subject has signed in at
/// all), so the gateway authenticates itself with a shared service credential (token): anyone able to reach the
/// service could otherwise set the password of any subject, or end its sessions. With no token configured every
/// request is refused.
#[derive(Clone)]
pub struct Handler {
    pub service: Arc<Service>,
    pub token: String,
}

/// The client interceptor of the gateway that presents the service credential ([`client_token`]).
#[derive(Debug, Clone)]
pub struct ClientToken(pub String);

/// ClientToken is the client option of the gateway that presents the service credential:
/// `Client::builder(url).interceptor(client_token(token))`.
pub fn client_token(token: &str) -> ClientToken {
    ClientToken(token.to_string())
}

#[async_trait]
impl Interceptor for ClientToken {
    async fn unary(
        &self,
        ctx: Ctx,
        mut req: AnyRequest,
        next: Next<'_>,
    ) -> Result<AnyResponse, ConnectError> {
        if req.spec.is_client
            && let Ok(v) = http::HeaderValue::from_str(&self.0)
        {
            req.headers.insert(TOKEN_HEADER, v);
        }
        next.run(ctx, req).await
    }
}

/// `subtle.ConstantTimeCompare(a, b) == 1`.
fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

impl Handler {
    pub fn new(service: Arc<Service>, token: &str) -> Self {
        Handler {
            service,
            token: token.to_string(),
        }
    }

    /// authn refuses a request that does not carry the service credential.
    fn authn(&self, hdr: &HeaderMap) -> Result<(), ConnectError> {
        let got = hdr
            .get(TOKEN_HEADER)
            .map(|v| v.as_bytes())
            .unwrap_or_default();
        if self.token.is_empty() || !constant_time_eq(got, self.token.as_bytes()) {
            return Err(ConnectError::unauthenticated(
                "credentials: the service credential is missing or invalid",
            ));
        }
        Ok(())
    }
}

fn rpc_err(err: Error) -> ConnectError {
    let code = match &err {
        Error::Invalid(_) => Code::InvalidArgument,
        Error::Exists => Code::AlreadyExists,
        Error::NotFound => Code::NotFound,
        _ => Code::Internal,
    };
    ConnectError::from_error(code, err)
}

#[async_trait]
impl pb::CredentialsService for Handler {
    async fn register(
        &self,
        ctx: &Ctx,
        r: Request<pb::RegisterRequest>,
    ) -> Result<Response<pb::RegisterResponse>, ConnectError> {
        self.authn(&r.headers)?;
        self.service
            .register(ctx, &r.msg.subject, &r.msg.password)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::RegisterResponse {}))
    }

    async fn verify(
        &self,
        ctx: &Ctx,
        r: Request<pb::VerifyRequest>,
    ) -> Result<Response<pb::VerifyResponse>, ConnectError> {
        self.authn(&r.headers)?;
        let ok = self
            .service
            .verify(ctx, &r.msg.subject, &r.msg.password)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::VerifyResponse { ok }))
    }

    async fn set_password(
        &self,
        ctx: &Ctx,
        r: Request<pb::SetPasswordRequest>,
    ) -> Result<Response<pb::SetPasswordResponse>, ConnectError> {
        self.authn(&r.headers)?;
        self.service
            .set_password(ctx, &r.msg.subject, &r.msg.password)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::SetPasswordResponse {}))
    }

    async fn exists(
        &self,
        ctx: &Ctx,
        r: Request<pb::ExistsRequest>,
    ) -> Result<Response<pb::ExistsResponse>, ConnectError> {
        self.authn(&r.headers)?;
        let exists = self
            .service
            .exists(ctx, &r.msg.subject)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::ExistsResponse { exists }))
    }

    async fn start_session(
        &self,
        ctx: &Ctx,
        r: Request<pb::StartSessionRequest>,
    ) -> Result<Response<pb::StartSessionResponse>, ConnectError> {
        self.authn(&r.headers)?;
        let secs = u64::try_from(r.msg.max_age_seconds).unwrap_or(0);
        let id = self
            .service
            .start_session(ctx, &r.msg.subject, std::time::Duration::from_secs(secs))
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::StartSessionResponse { id }))
    }

    async fn check_session(
        &self,
        ctx: &Ctx,
        r: Request<pb::CheckSessionRequest>,
    ) -> Result<Response<pb::CheckSessionResponse>, ConnectError> {
        self.authn(&r.headers)?;
        let active = self
            .service
            .session_active(ctx, &r.msg.id, &r.msg.subject)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::CheckSessionResponse { active }))
    }

    async fn end_session(
        &self,
        ctx: &Ctx,
        r: Request<pb::EndSessionRequest>,
    ) -> Result<Response<pb::EndSessionResponse>, ConnectError> {
        self.authn(&r.headers)?;
        self.service
            .end_session(ctx, &r.msg.id)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::EndSessionResponse {}))
    }

    async fn end_sessions(
        &self,
        ctx: &Ctx,
        r: Request<pb::EndSessionsRequest>,
    ) -> Result<Response<pb::EndSessionsResponse>, ConnectError> {
        self.authn(&r.headers)?;
        self.service
            .end_sessions(ctx, &r.msg.subject)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::EndSessionsResponse {}))
    }
}
