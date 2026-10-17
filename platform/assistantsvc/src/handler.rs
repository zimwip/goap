//! The Connect handler of `assistant.v1.AssistantService` (`handler.go`). It is a service of its own rather than an
//! RPC of the conversation service: that one only stores, and must stay free of the graph and the model gateway.

use std::sync::Arc;

use async_trait::async_trait;
use goap_connect::{Code, ConnectError, Ctx, Request, Response};
use goap_convsvc::message_to_pb;
use goap_gen::assistant::v1 as pb;
use goap_identity::Extractor;

use crate::agents::ConfirmInput;
use crate::context::{App, Context, Dialog, Element, Entity, Focus, Screen, Tab};
use crate::service::{Error, SendInput, Service};
use crate::uitools::{ReportInput, UIArgs, UIParam, UITool};

/// Handler implements `AssistantService`.
pub struct Handler {
    pub service: Arc<Service>,
    pub identity: Extractor,
}

/// rpcErr is the Connect error of an error of the service. As in the Go, an ErrState (and any other error) is
/// Unavailable.
pub fn rpc_err(err: &Error) -> ConnectError {
    if let Error::Model(goap_modelgw::Error::Rpc(ce)) = err {
        return ce.clone();
    }
    if let Error::Conv(e) = err
        && let Some(ce) = e.downcast_other::<ConnectError>()
    {
        return ce.clone();
    }
    let code = if err.is_anonymous() {
        Code::Unauthenticated
    } else if err.is_forbidden() {
        Code::PermissionDenied
    } else if err.is_not_found() {
        Code::NotFound
    } else if err.is_invalid() {
        Code::InvalidArgument
    } else if err.is_decided() {
        Code::Aborted
    } else if err.is_stale()
        || err.is_unavailable()
        || err.is_busy()
        || matches!(err, Error::Model(e) if e.is_model_disabled())
    {
        Code::FailedPrecondition
    } else {
        Code::Unavailable
    };
    ConnectError::new(code, err.to_string())
}

#[async_trait]
impl pb::AssistantService for Handler {
    async fn send(
        &self,
        ctx: &Ctx,
        r: Request<pb::SendRequest>,
    ) -> Result<Response<pb::SendResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let msg = r.msg;
        let in_ = SendInput {
            conversation_id: msg.conversation_id,
            text: msg.text,
            context: msg
                .context
                .as_ref()
                .map(context_from_pb)
                .unwrap_or_default(),
            ui_tools: msg.ui_tools.iter().map(tool_from_pb).collect(),
        };
        let (user, pending) = self
            .service
            .send(&ctx, in_)
            .await
            .map_err(|e| rpc_err(&e))?;
        Ok(Response::new(pb::SendResponse {
            user_message: Some(message_to_pb(&user)),
            assistant_message: Some(message_to_pb(&pending)),
        }))
    }

    async fn confirm_action(
        &self,
        ctx: &Ctx,
        r: Request<pb::ConfirmActionRequest>,
    ) -> Result<Response<pb::ConfirmActionResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let msg = r.msg;
        let m = self
            .service
            .confirm(
                &ctx,
                ConfirmInput {
                    conversation_id: msg.conversation_id,
                    message_id: msg.message_id,
                    action_index: i64::from(msg.action_index),
                    decision: msg.decision,
                    project: msg.project,
                },
            )
            .await
            .map_err(|e| rpc_err(&e))?;
        Ok(Response::new(pb::ConfirmActionResponse {
            message: Some(message_to_pb(&m)),
        }))
    }

    async fn report_action(
        &self,
        ctx: &Ctx,
        r: Request<pb::ReportActionRequest>,
    ) -> Result<Response<pb::ReportActionResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let msg = r.msg;
        let m = self
            .service
            .report(
                &ctx,
                ReportInput {
                    conversation_id: msg.conversation_id,
                    message_id: msg.message_id,
                    action_index: i64::from(msg.action_index),
                    status: msg.status,
                    error: msg.error,
                },
            )
            .await
            .map_err(|e| rpc_err(&e))?;
        Ok(Response::new(pb::ReportActionResponse {
            message: Some(message_to_pb(&m)),
        }))
    }

    async fn cancel(
        &self,
        ctx: &Ctx,
        r: Request<pb::CancelRequest>,
    ) -> Result<Response<pb::CancelResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let m = self
            .service
            .cancel(&ctx, &r.msg.conversation_id)
            .await
            .map_err(|e| rpc_err(&e))?;
        Ok(Response::new(pb::CancelResponse {
            message: Some(message_to_pb(&m)),
        }))
    }
}

pub(crate) fn context_from_pb(c: &pb::PageContext) -> Context {
    let mut o = Context::default();
    if let Some(a) = &c.app {
        o.app = App {
            project: a.project.clone(),
            tab: a
                .tab
                .as_ref()
                .map(|t| Tab {
                    kind: t.kind.clone(),
                    params: t.params.clone().into_iter().collect(),
                })
                .unwrap_or_default(),
        };
    }
    if let Some(s) = &c.screen {
        o.screen = Screen {
            kind: s.kind.clone(),
            title: s.title.clone(),
            summary: s.summary.clone(),
            entities: s
                .entities
                .iter()
                .map(|e| Entity {
                    r#type: e.r#type.clone(),
                    id: e.id.clone(),
                    label: e.label.clone(),
                    state: e.state.clone(),
                    props: e.props.clone().into_iter().collect(),
                })
                .collect(),
        };
    }
    if let Some(f) = &c.focus {
        o.focus = Focus {
            element: f.element.as_ref().map(|e| Element {
                r#type: e.r#type.clone(),
                id: e.id.clone(),
                label: e.label.clone(),
            }),
            selection: f.selection.clone(),
            dialog: Dialog {
                kind: f.dialog_kind.clone(),
                title: f.dialog_title.clone(),
            },
            pending_action: f.pending_action.clone(),
            errors: f.errors.clone(),
            last_action: f.last_action.clone(),
        };
    }
    o
}

pub(crate) fn param_from_pb(p: &pb::UiParam) -> UIParam {
    UIParam {
        r#type: p.r#type.clone(),
        description: p.description.clone(),
        r#enum: p.r#enum.clone(),
        items: p.items.as_deref().map(|i| Box::new(param_from_pb(i))),
    }
}

pub(crate) fn tool_from_pb(t: &pb::UiTool) -> UITool {
    let mut out = UITool {
        name: t.name.clone(),
        description: t.description.clone(),
        guidance: t.guidance.clone(),
        level: t.level.clone(),
        target: t.target.clone(),
        args: UIArgs::default(),
    };
    if let Some(a) = &t.args {
        out.args.required = a.required.clone();
        for (k, p) in &a.properties {
            out.args.properties.insert(k.clone(), param_from_pb(p));
        }
    }
    out
}
