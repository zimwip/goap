//! What the proxy reads of a Connect unary call: its procedure, its codec, its message as JSON (through the
//! descriptors of proto/, so any method of any service is read without code of its own) and its error.
use std::sync::LazyLock;

use bytes::Bytes;
use http::{HeaderMap, StatusCode, header::CONTENT_TYPE};
use prost::Message as _;
use prost_reflect::{DescriptorPool, DeserializeOptions, DynamicMessage, MessageDescriptor, SerializeOptions};
use serde_json::Value;

static POOL: LazyLock<DescriptorPool> =
    LazyLock::new(|| DescriptorPool::decode(goap_proto::DESCRIPTOR).expect("descriptors of proto/"));

/// A procedure: `/goap.graph.v1.GraphService/GetNode`.
#[derive(Debug, Clone, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct Procedure {
    pub service: String,
    pub method: String,
}

impl Procedure {
    /// The procedure of a path, when it names a method of proto/.
    pub fn of(path: &str) -> Option<Procedure> {
        let (service, method) = path.trim_start_matches('/').split_once('/')?;
        let svc = POOL.get_service_by_name(service)?;
        svc.methods().find(|m| m.name() == method)?;
        Some(Procedure {
            service: service.to_string(),
            method: method.to_string(),
        })
    }

    fn method(&self) -> prost_reflect::MethodDescriptor {
        let svc = POOL.get_service_by_name(&self.service).expect("known service");
        svc.methods().find(|m| m.name() == self.method).expect("known method")
    }

    pub fn input(&self) -> MessageDescriptor {
        self.method().input()
    }

    pub fn output(&self) -> MessageDescriptor {
        self.method().output()
    }
}

impl std::fmt::Display for Procedure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "/{}/{}", self.service, self.method)
    }
}

/// The codec of a unary call.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Codec {
    Proto,
    Json,
}

impl Codec {
    pub fn of(headers: &HeaderMap) -> Option<Codec> {
        let ct = headers.get(CONTENT_TYPE)?.to_str().ok()?;
        let ct = ct.split(';').next().unwrap_or("").trim();
        match ct {
            "application/proto" => Some(Codec::Proto),
            "application/json" => Some(Codec::Json),
            _ => None,
        }
    }
}

fn serialize_options() -> SerializeOptions {
    // the defaults of protojson: names in lowerCamelCase, 64-bit integers as strings, default values left out
    SerializeOptions::new()
}

/// A message decoded to its protobuf JSON form.
pub fn to_json(desc: &MessageDescriptor, codec: Codec, body: &[u8]) -> anyhow::Result<Value> {
    let msg = match codec {
        Codec::Proto => DynamicMessage::decode(desc.clone(), body)?,
        Codec::Json => {
            let mut de = serde_json::Deserializer::from_slice(body);
            DynamicMessage::deserialize_with_options(
                desc.clone(),
                &mut de,
                &DeserializeOptions::new().deny_unknown_fields(false),
            )?
        }
    };
    Ok(msg.serialize_with_options(serde_json::value::Serializer, &serialize_options())?)
}

/// A JSON form encoded back in a codec.
pub fn from_json(desc: &MessageDescriptor, codec: Codec, v: &Value) -> anyhow::Result<Bytes> {
    match codec {
        Codec::Json => Ok(Bytes::from(serde_json::to_vec(v)?)),
        Codec::Proto => {
            let msg = DynamicMessage::deserialize_with_options(
                desc.clone(),
                v.clone(),
                &DeserializeOptions::new().deny_unknown_fields(false),
            )?;
            Ok(Bytes::from(msg.encode_to_vec()))
        }
    }
}

/// The error of a unary call: its code and message (`{"code","message","details"}`), or the code of its HTTP status
/// when the body is not one (an unknown route answers 404, read as `unimplemented` as connect-go reads it).
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct RpcError {
    pub code: String,
    pub message: String,
}

pub fn error_of(status: StatusCode, body: &[u8]) -> Option<RpcError> {
    if status == StatusCode::OK {
        return None;
    }
    if let Ok(Value::Object(o)) = serde_json::from_slice::<Value>(body)
        && let Some(Value::String(code)) = o.get("code")
    {
        let message = o.get("message").and_then(Value::as_str).unwrap_or("").to_string();
        return Some(RpcError {
            code: code.clone(),
            message,
        });
    }
    let code = match status.as_u16() {
        400 => "invalid_argument",
        401 => "unauthenticated",
        403 => "permission_denied",
        404 | 501 => "unimplemented",
        408 => "deadline_exceeded",
        409 => "aborted",
        412 => "failed_precondition",
        413 => "resource_exhausted",
        429 | 502..=504 => "unavailable",
        _ => "unknown",
    };
    Some(RpcError {
        code: code.to_string(),
        message: String::from_utf8_lossy(body).into_owned(),
    })
}

/// The methods of a service of proto/.
pub fn methods_of(service: &str) -> Vec<String> {
    POOL.get_service_by_name(service)
        .map(|s| s.methods().map(|m| m.name().to_string()).collect())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_a_procedure_of_proto() {
        let p = Procedure::of("/goap.graph.v1.GraphService/GetNode").unwrap();
        assert_eq!(p.input().name(), "GetNodeRequest");
        assert!(Procedure::of("/goap.graph.v1.GraphService/Nope").is_none());
        assert!(Procedure::of("/healthz").is_none());
    }

    #[test]
    fn a_message_goes_through_both_codecs() {
        let p = Procedure::of("/goap.graph.v1.GraphService/GetNode").unwrap();
        let v = serde_json::json!({"key": "R-1", "version": "3"});
        let bin = from_json(&p.input(), Codec::Proto, &v).unwrap();
        assert_eq!(to_json(&p.input(), Codec::Proto, &bin).unwrap()["key"], "R-1");
        let js = from_json(&p.input(), Codec::Json, &v).unwrap();
        assert_eq!(to_json(&p.input(), Codec::Json, &js).unwrap()["key"], "R-1");
    }

    #[test]
    fn reads_an_error() {
        let e = error_of(
            StatusCode::NOT_IMPLEMENTED,
            br#"{"code":"unimplemented","message":"x"}"#,
        )
        .unwrap();
        assert_eq!(e.code, "unimplemented");
        assert_eq!(
            error_of(StatusCode::NOT_FOUND, b"404 page not found").unwrap().code,
            "unimplemented"
        );
        assert!(error_of(StatusCode::OK, b"").is_none());
    }
}
