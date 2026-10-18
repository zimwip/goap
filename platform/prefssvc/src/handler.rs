use std::{collections::HashMap, sync::Arc};

use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_gen::preferences::v1 as pb;
use goap_identity::Extractor;
use pbjson_types::value::Kind;
use serde_json::{Map, Value};

use crate::{
    service::{Error, Service},
    store::Prefs,
};

/// Handler implements the Connect `PreferencesService`. The subject of a request is the caller's: there is no way to
/// name another user, so no one reads or writes the preferences of someone else.
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
    match &err {
        Error::Anonymous => ConnectError::from_error(goap_connect::Code::Unauthenticated, err),
        Error::Invalid(_) => ConnectError::from_error(goap_connect::Code::InvalidArgument, err),
        _ => ConnectError::from_error(goap_connect::Code::Internal, err),
    }
}

/// `structpb.NewStruct` of a document.
fn to_struct(p: &Prefs) -> pbjson_types::Struct {
    pbjson_types::Struct {
        fields: p
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
fn to_map(s: Option<&pbjson_types::Struct>) -> Map<String, Value> {
    s.map(|s| {
        s.fields
            .iter()
            .map(|(k, v)| (k.clone(), value_to_json(v)))
            .collect()
    })
    .unwrap_or_default()
}

fn value_to_json(v: &pbjson_types::Value) -> Value {
    match &v.kind {
        None | Some(Kind::NullValue(_)) => Value::Null,
        Some(Kind::BoolValue(b)) => Value::Bool(*b),
        Some(Kind::NumberValue(f)) => number(*f),
        Some(Kind::StringValue(s)) => Value::String(s.clone()),
        Some(Kind::ListValue(l)) => Value::Array(l.values.iter().map(value_to_json).collect()),
        Some(Kind::StructValue(s)) => Value::Object(to_map(Some(s))),
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

#[async_trait]
impl pb::PreferencesService for Handler {
    async fn get_preferences(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetPreferencesRequest>,
    ) -> Result<Response<pb::GetPreferencesResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let subject = goap_authz::principal_from(&ctx).subject;
        let p = self.service.get(&ctx, &subject).await.map_err(rpc_err)?;
        Ok(Response::new(pb::GetPreferencesResponse {
            values: Some(to_struct(&p)),
        }))
    }

    async fn set_preferences(
        &self,
        ctx: &Ctx,
        r: Request<pb::SetPreferencesRequest>,
    ) -> Result<Response<pb::SetPreferencesResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let subject = goap_authz::principal_from(&ctx).subject;
        let patch = to_map(r.msg.values.as_ref());
        let p = self
            .service
            .set(&ctx, &subject, patch)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(pb::SetPreferencesResponse {
            values: Some(to_struct(&p)),
        }))
    }

    async fn reset_preferences(
        &self,
        ctx: &Ctx,
        r: Request<pb::ResetPreferencesRequest>,
    ) -> Result<Response<pb::ResetPreferencesResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let subject = goap_authz::principal_from(&ctx).subject;
        self.service.reset(&ctx, &subject).await.map_err(rpc_err)?;
        Ok(Response::new(pb::ResetPreferencesResponse {}))
    }
}
