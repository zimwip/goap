//! The messages the runtime carries and their two codecs.
use std::{any::Any, fmt};

use crate::error::ConnectError;

/// A message of the generated contracts: prost for the binary form, serde (pbjson) for the protobuf JSON form.
/// Every `goap_proto` message is one.
pub trait Message:
    prost::Message + Default + std::fmt::Debug + serde::Serialize + serde::de::DeserializeOwned + Send + Sync + 'static
{
}

impl<T> Message for T where
    T: prost::Message
        + Default
        + std::fmt::Debug
        + serde::Serialize
        + serde::de::DeserializeOwned
        + Send
        + Sync
        + 'static
{
}

/// The encoding of the messages of a call.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Codec {
    /// `application/proto` (unary), `application/connect+proto` (streams): the binary form. The default of the
    /// clients, as connect-go's.
    #[default]
    Proto,
    /// `application/json` (unary), `application/connect+json` (streams): the protobuf JSON form, what the web sends.
    Json,
}

impl Codec {
    /// The codec name: `proto` or `json`.
    pub fn name(self) -> &'static str {
        match self {
            Codec::Proto => "proto",
            Codec::Json => "json",
        }
    }

    /// The content type of a unary call.
    pub fn unary_content_type(self) -> &'static str {
        match self {
            Codec::Proto => "application/proto",
            Codec::Json => "application/json",
        }
    }

    /// The content type of a streaming call.
    pub fn stream_content_type(self) -> &'static str {
        match self {
            Codec::Proto => "application/connect+proto",
            Codec::Json => "application/connect+json",
        }
    }

    /// The codec of a unary content type (`application/json; charset=utf-8` included).
    pub fn from_unary_content_type(ct: &str) -> Option<Codec> {
        match media_type(ct).as_str() {
            "application/proto" => Some(Codec::Proto),
            "application/json" => Some(Codec::Json),
            _ => None,
        }
    }

    /// The codec of a streaming content type.
    pub fn from_stream_content_type(ct: &str) -> Option<Codec> {
        match media_type(ct).as_str() {
            "application/connect+proto" => Some(Codec::Proto),
            "application/connect+json" => Some(Codec::Json),
            _ => None,
        }
    }

    /// Encodes a message.
    pub fn encode<M: Message>(self, m: &M) -> Result<Vec<u8>, ConnectError> {
        match self {
            Codec::Proto => Ok(m.encode_to_vec()),
            Codec::Json => serde_json::to_vec(m).map_err(|e| ConnectError::internal(format!("marshal message: {e}"))),
        }
    }

    /// Decodes a message; an empty body is the empty message. A JSON field the message does not know is ignored
    /// (connect-go reads JSON with `DiscardUnknown`, through the generator's `ignore_unknown_fields`).
    pub fn decode<M: Message>(self, b: &[u8]) -> Result<M, ConnectError> {
        if b.is_empty() || (self == Codec::Json && b.iter().all(u8::is_ascii_whitespace)) {
            return Ok(M::default());
        }
        match self {
            Codec::Proto => M::decode(b).map_err(|e| ConnectError::invalid_argument(format!("unmarshal message: {e}"))),
            Codec::Json => {
                serde_json::from_slice(b).map_err(|e| ConnectError::invalid_argument(format!("unmarshal message: {e}")))
            }
        }
    }
}

fn media_type(ct: &str) -> String {
    ct.split(';').next().unwrap_or("").trim().to_ascii_lowercase()
}

/// A message whose type is erased, as interceptors see it (connect-go's `req.Any()`): downcast it with
/// [`AnyMessage::as_any`], or read it as JSON with [`AnyMessage::to_json`] (the protobuf JSON form: lowerCamelCase
/// fields, defaults left out), which stands for Go's protoreflect.
pub trait AnyMessage: Send + Sync + fmt::Debug + 'static {
    fn as_any(&self) -> &dyn Any;
    fn as_any_mut(&mut self) -> &mut dyn Any;
    fn into_any(self: Box<Self>) -> Box<dyn Any + Send>;
    /// The protobuf JSON form of the message (`Null` if it cannot be written).
    fn to_json(&self) -> serde_json::Value;
    /// Encodes the message.
    fn encode_with(&self, codec: Codec) -> Result<Vec<u8>, ConnectError>;
    /// The Rust type name of the message.
    fn type_name(&self) -> &'static str;
}

impl<T: Message> AnyMessage for T {
    fn as_any(&self) -> &dyn Any {
        self
    }
    fn as_any_mut(&mut self) -> &mut dyn Any {
        self
    }
    fn into_any(self: Box<Self>) -> Box<dyn Any + Send> {
        self
    }
    fn to_json(&self) -> serde_json::Value {
        serde_json::to_value(self).unwrap_or(serde_json::Value::Null)
    }
    fn encode_with(&self, codec: Codec) -> Result<Vec<u8>, ConnectError> {
        codec.encode(self)
    }
    fn type_name(&self) -> &'static str {
        std::any::type_name::<T>()
    }
}

impl dyn AnyMessage {
    /// The message as a `T`.
    pub fn downcast_ref<T: 'static>(&self) -> Option<&T> {
        self.as_any().downcast_ref()
    }

    pub fn downcast_mut<T: 'static>(&mut self) -> Option<&mut T> {
        self.as_any_mut().downcast_mut()
    }

    /// Takes the message back as a `T`.
    pub fn downcast<T: 'static>(self: Box<Self>) -> Result<T, ConnectError> {
        let name = self.type_name();
        self.into_any()
            .downcast::<T>()
            .map(|b| *b)
            .map_err(|_| ConnectError::internal(format!("message is a {name}, not a {}", std::any::type_name::<T>())))
    }
}
