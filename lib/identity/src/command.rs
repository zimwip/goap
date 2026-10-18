use goap_connect::{AnyRequest, AnyResponse, ConnectError, Ctx, Interceptor, Next, async_trait};
use goap_platform::{HEADER_COMMAND, Stamp, with_stamp};

use crate::identity::from_headers;

/// CommandInterceptor stamps the events published while a request is served with the id its client gave it
/// (X-Goap-Command) and the caller, so a client recognises the echo of its own writes (ADR 0053). Wrap the services
/// that write. As connect-go's `UnaryInterceptorFunc` it leaves streams alone.
#[derive(Debug, Clone, Copy, Default)]
pub struct CommandInterceptor;

/// `CommandInterceptor()`.
pub fn command_interceptor() -> CommandInterceptor {
    CommandInterceptor
}

#[async_trait]
impl Interceptor for CommandInterceptor {
    async fn unary(
        &self,
        ctx: Ctx,
        req: AnyRequest,
        next: Next<'_>,
    ) -> Result<AnyResponse, ConnectError> {
        let c = req.header(HEADER_COMMAND);
        let ctx = if c.is_empty() {
            ctx
        } else {
            let stamp = Stamp {
                actor: from_headers(&req.headers).subject,
                command: c.to_string(),
            };
            with_stamp(&ctx, stamp)
        };
        next.run(ctx, req).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_connect::axum::http::{HeaderMap, HeaderName, HeaderValue};
    use goap_connect::{Interceptors, Spec, UnaryEnd};
    use goap_platform::stamp_of;
    use std::sync::{Arc, Mutex};

    #[tokio::test]
    async fn command_interceptor_stamps_the_context() {
        let seen: Arc<Mutex<Option<Stamp>>> = Arc::default();
        let s = seen.clone();
        let end: Box<UnaryEnd> = Box::new(move |ctx: Ctx, _req: AnyRequest| {
            *s.lock().unwrap() = Some(stamp_of(&ctx));
            Box::pin(async move {
                Ok(AnyResponse {
                    headers: HeaderMap::new(),
                    trailers: HeaderMap::new(),
                    msg: Box::new(()),
                })
            })
        });
        let chain = Interceptors::none().with(command_interceptor());
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::try_from(HEADER_COMMAND).unwrap(),
            HeaderValue::from_static("cmd-1"),
        );
        headers.insert(
            HeaderName::try_from(crate::HEADER_SUBJECT).unwrap(),
            HeaderValue::from_static("alice"),
        );
        let req = AnyRequest {
            spec: Spec::default(),
            headers,
            msg: Box::new(()),
        };
        chain
            .run_unary(Ctx::background(), req, &*end)
            .await
            .unwrap();
        assert_eq!(
            seen.lock().unwrap().clone().unwrap(),
            Stamp {
                actor: "alice".into(),
                command: "cmd-1".into()
            }
        );
        let req = AnyRequest {
            spec: Spec::default(),
            headers: HeaderMap::new(),
            msg: Box::new(()),
        };
        chain
            .run_unary(Ctx::background(), req, &*end)
            .await
            .unwrap();
        assert_eq!(seen.lock().unwrap().clone().unwrap(), Stamp::default());
    }
}
