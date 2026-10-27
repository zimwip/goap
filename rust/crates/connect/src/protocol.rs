//! The wire details of the Connect protocol: headers, timeouts, envelopes, end-of-stream messages.
use std::time::Duration;

use bytes::{Buf, BufMut, Bytes, BytesMut};
use http::{HeaderMap, HeaderValue};
use serde::{Deserialize, Serialize};

use crate::error::{ConnectError, WireError, headers_from_json, headers_to_json};

/// `Connect-Protocol-Version`: `1` when sent.
pub const HEADER_PROTOCOL_VERSION: &str = "connect-protocol-version";
/// `Connect-Timeout-Ms`: the time the client gives the call, in milliseconds (at most ten digits).
pub const HEADER_TIMEOUT: &str = "connect-timeout-ms";
/// The prefix of the trailers of a unary response, sent as headers.
pub const TRAILER_PREFIX: &str = "trailer-";
/// `Content-Encoding` of a unary call, `Connect-Content-Encoding` of a stream: only `identity` is supported.
pub const HEADER_CONTENT_ENCODING: &str = "content-encoding";
pub const HEADER_STREAM_CONTENT_ENCODING: &str = "connect-content-encoding";

/// The flag of a compressed envelope.
pub const FLAG_COMPRESSED: u8 = 0x01;
/// The flag of the end-of-stream envelope.
pub const FLAG_END_STREAM: u8 = 0x02;

/// Reads `Connect-Timeout-Ms` (None: absent).
pub fn parse_timeout(h: &HeaderMap) -> Result<Option<Duration>, ConnectError> {
    let Some(v) = h.get(HEADER_TIMEOUT) else {
        return Ok(None);
    };
    let s = v.to_str().unwrap_or("");
    if s.is_empty() || s.len() > 10 || !s.bytes().all(|b| b.is_ascii_digit()) {
        return Err(ConnectError::invalid_argument(format!(
            "protocol error: invalid timeout {s:?}"
        )));
    }
    let ms: u64 = s
        .parse()
        .map_err(|_| ConnectError::invalid_argument(format!("protocol error: invalid timeout {s:?}")))?;
    Ok(Some(Duration::from_millis(ms)))
}

/// The value of `Connect-Timeout-Ms` for what is left of a deadline (at least 1 ms, at most ten digits).
pub fn timeout_value(left: Duration) -> HeaderValue {
    let ms = left.as_millis().clamp(1, 9_999_999_999);
    HeaderValue::from_str(&ms.to_string()).unwrap_or_else(|_| HeaderValue::from_static("1"))
}

/// Refuses a `Connect-Protocol-Version` other than `1` (an absent header is accepted, as connect-go does).
pub fn check_protocol_version(h: &HeaderMap) -> Result<(), ConnectError> {
    match h.get(HEADER_PROTOCOL_VERSION) {
        None => Ok(()),
        Some(v) if v.as_bytes() == b"1" => Ok(()),
        Some(v) => Err(ConnectError::invalid_argument(format!(
            "{HEADER_PROTOCOL_VERSION} must be \"1\": got {:?}",
            v.to_str().unwrap_or("")
        ))),
    }
}

/// The compression the server reads and writes besides `identity`, as connect-go registers it.
pub const GZIP: &str = "gzip";
/// `Accept-Encoding` of a unary call, `Connect-Accept-Encoding` of a stream: the compressions the peer reads.
pub const HEADER_ACCEPT_ENCODING: &str = "accept-encoding";
pub const HEADER_STREAM_ACCEPT_ENCODING: &str = "connect-accept-encoding";
/// The size under which an answer goes uncompressed, the `compressMinBytes` the Go services give connect-go
/// (`lib/telemetry`): resetting a gzip writer costs more than the bytes of a smaller answer save.
pub const COMPRESS_MIN_BYTES: usize = 1024;

/// Whether the body is gzip (`Content-Encoding` / `Connect-Content-Encoding`); another compression than identity and
/// gzip is refused in connect-go's words.
pub fn check_encoding(h: &HeaderMap, name: &str) -> Result<bool, ConnectError> {
    match h.get(name).and_then(|v| v.to_str().ok()).map(str::trim) {
        None | Some("") | Some("identity") => Ok(false),
        Some(GZIP) => Ok(true),
        Some(other) => Err(ConnectError::unimplemented(format!(
            "unknown compression {other:?}: supported encodings are {GZIP}"
        ))),
    }
}

/// Whether the peer reads gzip answers (connect-go's negotiation: the first name of the list it supports).
pub fn accepts_gzip(h: &HeaderMap, name: &str) -> bool {
    h.get_all(name)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|v| v.split([',', ' ']))
        .any(|n| n == GZIP)
}

/// The gzip of a payload, at the default level (Go's `gzip.NewWriter`).
pub fn gzip(data: &[u8]) -> Bytes {
    use std::io::Write;
    let mut w = flate2::write::GzEncoder::new(Vec::with_capacity(data.len() / 4 + 32), flate2::Compression::default());
    let _ = w.write_all(data);
    Bytes::from(w.finish().unwrap_or_default())
}

/// The payload of a gzip body, refused in connect-go's words when it is not one.
pub fn gunzip(data: &[u8]) -> Result<Bytes, ConnectError> {
    use std::io::Read;
    let mut out = Vec::with_capacity(data.len() * 4);
    flate2::read::GzDecoder::new(data)
        .read_to_end(&mut out)
        .map_err(|e| ConnectError::invalid_argument(format!("decompress: {e}")))?;
    Ok(Bytes::from(out))
}

/// One envelope: the flags, the 4-byte big-endian length, the payload.
pub fn envelope(flags: u8, payload: &[u8]) -> Bytes {
    let mut b = BytesMut::with_capacity(5 + payload.len());
    b.put_u8(flags);
    b.put_u32(payload.len() as u32);
    b.put_slice(payload);
    b.freeze()
}

/// Reads envelopes out of the chunks of a body.
#[derive(Debug, Default)]
pub struct EnvelopeReader {
    buf: BytesMut,
}

impl EnvelopeReader {
    pub fn new() -> Self {
        Self::default()
    }

    /// Adds a chunk of the body.
    pub fn push(&mut self, chunk: &[u8]) {
        self.buf.extend_from_slice(chunk);
    }

    /// The next whole envelope (flags, payload), None while it is incomplete.
    pub fn next_envelope(&mut self) -> Option<(u8, Bytes)> {
        if self.buf.len() < 5 {
            return None;
        }
        let len = u32::from_be_bytes([self.buf[1], self.buf[2], self.buf[3], self.buf[4]]) as usize;
        if self.buf.len() < 5 + len {
            return None;
        }
        let flags = self.buf[0];
        self.buf.advance(5);
        Some((flags, self.buf.split_to(len).freeze()))
    }

    /// Bytes left that are no whole envelope.
    pub fn remaining(&self) -> usize {
        self.buf.len()
    }
}

#[derive(Debug, Default, Serialize, Deserialize)]
struct EndStream {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    error: Option<WireError>,
    #[serde(default, skip_serializing_if = "serde_json::Map::is_empty")]
    metadata: serde_json::Map<String, serde_json::Value>,
}

/// The end-of-stream envelope: the error (its metadata merged into the trailers, as connect-go does) and the
/// trailers, always in JSON.
pub fn end_stream(err: Option<&ConnectError>, trailers: &HeaderMap) -> Bytes {
    let mut meta = trailers.clone();
    if let Some(e) = err {
        for (k, v) in e.meta() {
            meta.append(k.clone(), v.clone());
        }
    }
    let end = EndStream {
        error: err.map(ConnectError::to_wire),
        metadata: headers_to_json(&meta),
    };
    envelope(
        FLAG_END_STREAM,
        &serde_json::to_vec(&end).unwrap_or_else(|_| b"{}".to_vec()),
    )
}

/// Reads an end-of-stream payload: the trailers, and the error it ends with (its metadata the trailers).
pub fn read_end_stream(payload: &[u8]) -> Result<(HeaderMap, Option<ConnectError>), ConnectError> {
    let end: EndStream = if payload.iter().all(u8::is_ascii_whitespace) {
        EndStream::default()
    } else {
        serde_json::from_slice(payload)
            .map_err(|e| ConnectError::internal(format!("protocol error: invalid end-of-stream message: {e}")))?
    };
    let trailers = headers_from_json(&end.metadata);
    let err = end.error.map(|w| ConnectError::from_wire(w, trailers.clone()));
    Ok((trailers, err))
}

/// Reads the body of a unary error, `{"code","message","details"}`; a body that is not one gives the code of the
/// HTTP status.
pub fn read_unary_error(status: http::StatusCode, headers: HeaderMap, body: &[u8]) -> ConnectError {
    match serde_json::from_slice::<WireError>(body) {
        Ok(w) if w.code.is_some() => ConnectError::from_wire(w, headers),
        _ => {
            let w = WireError {
                code: Some(crate::Code::from_http_status(status)),
                message: format!("HTTP status {status}"),
                details: vec![],
            };
            ConnectError::from_wire(w, headers)
        }
    }
}
