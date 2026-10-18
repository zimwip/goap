use std::{collections::HashMap, sync::Arc};

use chrono::{DateTime, Utc};
use goap_connect::{Code, ConnectError, Ctx, Request, Response, async_trait};
use goap_gen::conversations::v1 as pb;
use goap_identity::Extractor;
use pbjson_types::value::Kind;
use serde_json::{Map, Value};

use crate::{
    service::{Content, Error, Service},
    store::{Action, Conversation, Message},
};

/// Handler implements the Connect `ConversationService`. The subject of a request is the caller's: there is no way
/// to name another user, so no one reads or writes the conversations of someone else.
#[derive(Clone)]
pub struct Handler {
    pub service: Arc<Service>,
    pub identity: Extractor,
}

impl Handler {
    pub fn new(service: Arc<Service>) -> Self {
        Handler {
            service,
            identity: Extractor::default(),
        }
    }
}

fn rpc_err(err: Error) -> ConnectError {
    if let Some(ce) = err.downcast_other::<ConnectError>() {
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
    } else {
        Code::Internal
    };
    ConnectError::from_error(code, err)
}

fn ts(t: &DateTime<Utc>) -> Option<pbjson_types::Timestamp> {
    Some((*t).into())
}

fn to_conversation(c: &Conversation) -> pb::Conversation {
    pb::Conversation {
        id: c.id.clone(),
        subject: c.subject.clone(),
        title: c.title.clone(),
        created_at: ts(&c.created_at),
        updated_at: ts(&c.updated_at),
    }
}

/// `structpb.NewStruct`.
fn to_struct(m: &Map<String, Value>) -> pbjson_types::Struct {
    pbjson_types::Struct {
        fields: m
            .iter()
            .map(|(k, v)| (k.clone(), json_to_value(v)))
            .collect::<HashMap<_, _>>(),
    }
}

fn json_to_value(v: &Value) -> pbjson_types::Value {
    let kind = match v {
        Value::Null => Kind::NullValue(0),
        Value::Bool(b) => Kind::BoolValue(*b),
        Value::Number(n) => Kind::NumberValue(n.as_f64().unwrap_or_default()),
        Value::String(s) => Kind::StringValue(s.clone()),
        Value::Array(a) => Kind::ListValue(pbjson_types::ListValue {
            values: a.iter().map(json_to_value).collect(),
        }),
        Value::Object(m) => Kind::StructValue(to_struct(m)),
    };
    pbjson_types::Value { kind: Some(kind) }
}

/// `Struct.AsMap`: a number is a float64 in Go, written as an integer when it is one.
fn to_map(s: &pbjson_types::Struct) -> Map<String, Value> {
    s.fields
        .iter()
        .map(|(k, v)| (k.clone(), value_to_json(v)))
        .collect()
}

fn value_to_json(v: &pbjson_types::Value) -> Value {
    match &v.kind {
        None | Some(Kind::NullValue(_)) => Value::Null,
        Some(Kind::BoolValue(b)) => Value::Bool(*b),
        Some(Kind::NumberValue(f)) => number(*f),
        Some(Kind::StringValue(s)) => Value::String(s.clone()),
        Some(Kind::ListValue(l)) => Value::Array(l.values.iter().map(value_to_json).collect()),
        Some(Kind::StructValue(s)) => Value::Object(to_map(s)),
    }
}

fn number(f: f64) -> Value {
    const EXACT: f64 = 9_007_199_254_740_992.0; // 2^53
    if f.is_finite() && f.fract() == 0.0 && f.abs() <= EXACT && !(f == 0.0 && f.is_sign_negative())
    {
        return Value::from(f as i64);
    }
    serde_json::Number::from_f64(f)
        .map(Value::Number)
        .unwrap_or(Value::Null)
}

fn from_structs(input: &[pbjson_types::Struct]) -> Vec<Action> {
    input.iter().map(to_map).collect()
}

/// MessageToPB converts a message to its wire form (the assistant's Send returns the messages it appended).
pub fn message_to_pb(m: &Message) -> pb::Message {
    pb::Message {
        id: m.id.clone(),
        conversation_id: m.conversation_id.clone(),
        seq: m.seq as i32,
        role: m.role.clone(),
        text: m.text.clone(),
        actions: m.actions.iter().map(to_struct).collect(),
        process_id: m.process_id.clone(),
        status: m.status.clone(),
        error: m.error.clone(),
        context: m.context.clone(),
        created_at: ts(&m.created_at),
    }
}

#[async_trait]
impl pb::ConversationService for Handler {
    async fn create_conversation(
        &self,
        ctx: &Ctx,
        r: Request<pb::CreateConversationRequest>,
    ) -> Result<Response<pb::CreateConversationResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let p = goap_authz::principal_from(&ctx);
        let c = self
            .service
            .create(&ctx, &p, &r.msg.title)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::CreateConversationResponse {
            conversation: Some(to_conversation(&c)),
        }))
    }

    async fn list_conversations(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListConversationsRequest>,
    ) -> Result<Response<pb::ListConversationsResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let p = goap_authz::principal_from(&ctx);
        let (cs, next) = self
            .service
            .list(&ctx, &p, r.msg.page_size.into(), &r.msg.page_token)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::ListConversationsResponse {
            conversations: cs.iter().map(to_conversation).collect(),
            next_page_token: next,
        }))
    }

    async fn get_conversation(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetConversationRequest>,
    ) -> Result<Response<pb::GetConversationResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let p = goap_authz::principal_from(&ctx);
        let (c, ms) = self
            .service
            .get(&ctx, &p, &r.msg.id)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::GetConversationResponse {
            conversation: Some(to_conversation(&c)),
            messages: ms.iter().map(message_to_pb).collect(),
        }))
    }

    async fn rename_conversation(
        &self,
        ctx: &Ctx,
        r: Request<pb::RenameConversationRequest>,
    ) -> Result<Response<pb::RenameConversationResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let p = goap_authz::principal_from(&ctx);
        let c = self
            .service
            .rename(&ctx, &p, &r.msg.id, &r.msg.title)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::RenameConversationResponse {
            conversation: Some(to_conversation(&c)),
        }))
    }

    async fn delete_conversation(
        &self,
        ctx: &Ctx,
        r: Request<pb::DeleteConversationRequest>,
    ) -> Result<Response<pb::DeleteConversationResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let p = goap_authz::principal_from(&ctx);
        self.service
            .delete(&ctx, &p, &r.msg.id)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::DeleteConversationResponse {}))
    }

    async fn append_message(
        &self,
        ctx: &Ctx,
        r: Request<pb::AppendMessageRequest>,
    ) -> Result<Response<pb::AppendMessageResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let p = goap_authz::principal_from(&ctx);
        let m = r.msg;
        let content = Content {
            text: m.text,
            actions: from_structs(&m.actions),
            process_id: m.process_id,
            status: m.status,
            context: m.context,
            ..Default::default()
        };
        let msg = self
            .service
            .append(&ctx, &p, &m.conversation_id, &m.role, content)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::AppendMessageResponse {
            message: Some(message_to_pb(&msg)),
        }))
    }

    async fn update_message(
        &self,
        ctx: &Ctx,
        r: Request<pb::UpdateMessageRequest>,
    ) -> Result<Response<pb::UpdateMessageResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let p = goap_authz::principal_from(&ctx);
        let m = r.msg;
        let content = Content {
            text: m.text,
            actions: from_structs(&m.actions),
            process_id: m.process_id,
            status: m.status,
            error: m.error,
            ..Default::default()
        };
        let msg = self
            .service
            .update_message(&ctx, &p, &m.id, content)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::UpdateMessageResponse {
            message: Some(message_to_pb(&msg)),
        }))
    }
}
