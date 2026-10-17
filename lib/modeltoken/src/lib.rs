//! The token a tool that calls a model by itself (the Claude coding agent, ADR 0123) carries to the model gateway's
//! Anthropic endpoint, so its calls are rows of the ledger (ADR 0089) attributed to the person, the change and the
//! process they ran for (ADR 0114). It is minted by the MCP hub for each call of a connector that calls a model by
//! itself and checked by the gateway, both holding the same secret (`GOAP_MODEL_PROXY_SECRET`): the connector holds
//! no secret and can name no one else. It is short-lived and names what it may be used for, never more: a principal
//! of its own (a system subject acting for the end user), no roles.
//!
//! A token is `base64url(JSON claims) "." base64url(HMAC-SHA256(secret, payload))`, unpadded: URL-safe, it may be a
//! path segment.
use std::time::Duration;

use base64::{Engine as _, engine::general_purpose::URL_SAFE_NO_PAD as ENC};
use chrono::{DateTime, Utc};
use hmac::{Hmac, KeyInit, Mac};
use serde::{Deserialize, Serialize};
use sha2::Sha256;

/// What a token says: who the calls are made by and for, and what they run for.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Claims {
    /// The system subject making the calls (`system:<tool>`).
    #[serde(rename = "sub", default)]
    pub subject: String,
    /// The person the calls are made for (`authz.Principal.OnBehalfOf`).
    #[serde(rename = "eu", default, skip_serializing_if = "String::is_empty")]
    pub end_user: String,
    #[serde(rename = "org", default, skip_serializing_if = "String::is_empty")]
    pub org: String,
    #[serde(rename = "prj", default, skip_serializing_if = "String::is_empty")]
    pub project: String,
    #[serde(rename = "chg", default, skip_serializing_if = "String::is_empty")]
    pub change: String,
    #[serde(rename = "prc", default, skip_serializing_if = "String::is_empty")]
    pub process: String,
    /// The source of the calls in the ledger (`llm.CallMeta.Source`).
    #[serde(rename = "src", default, skip_serializing_if = "String::is_empty")]
    pub source: String,
    /// The Unix time after which the token is refused.
    #[serde(rename = "exp", default)]
    pub expires: i64,
}

/// The errors of [`sign`] and [`verify`].
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    #[error("invalid model token")]
    Invalid,
    #[error("model token expired")]
    Expired,
    #[error("model token: no secret")]
    NoSecret,
    #[error("model token: {0}")]
    Encode(String),
}

impl Error {
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid)
    }

    pub fn is_expired(&self) -> bool {
        matches!(self, Error::Expired)
    }
}

/// Returns the token of `c`, valid for `ttl` from `now`. The token is URL-safe (it may be a path segment).
pub fn sign(
    secret: &[u8],
    mut c: Claims,
    now: DateTime<Utc>,
    ttl: Duration,
) -> Result<String, Error> {
    if secret.is_empty() {
        return Err(Error::NoSecret);
    }
    let ttl = chrono::Duration::from_std(ttl).map_err(|e| Error::Encode(e.to_string()))?;
    c.expires = (now + ttl).timestamp();
    let body = serde_json::to_vec(&c).map_err(|e| Error::Encode(e.to_string()))?;
    let payload = ENC.encode(body);
    let sig = ENC.encode(mac(secret, &payload).finalize().into_bytes());
    Ok(format!("{payload}.{sig}"))
}

/// Checks the signature and the expiry of a token and returns its claims.
pub fn verify(secret: &[u8], token: &str, now: DateTime<Utc>) -> Result<Claims, Error> {
    if secret.is_empty() {
        return Err(Error::Invalid);
    }
    let (payload, sig) = token.split_once('.').ok_or(Error::Invalid)?;
    let got = ENC.decode(sig).map_err(|_| Error::Invalid)?;
    mac(secret, payload)
        .verify_slice(&got)
        .map_err(|_| Error::Invalid)?;
    let body = ENC.decode(payload).map_err(|_| Error::Invalid)?;
    let c: Claims = serde_json::from_slice(&body).map_err(|_| Error::Invalid)?;
    if c.subject.is_empty() {
        return Err(Error::Invalid);
    }
    if now.timestamp() > c.expires {
        return Err(Error::Expired);
    }
    Ok(c)
}

fn mac(secret: &[u8], payload: &str) -> Hmac<Sha256> {
    let mut h =
        <Hmac<Sha256> as KeyInit>::new_from_slice(secret).expect("HMAC takes a key of any size");
    h.update(payload.as_bytes());
    h
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sign_verify() {
        let secret = b"s3cret";
        let now = DateTime::from_timestamp(1_800_000_000, 0).unwrap();
        let c = Claims {
            subject: "system:claude-coding-agent".into(),
            end_user: "alice".into(),
            change: "C1".into(),
            process: "P1".into(),
            source: "claude-coding-agent".into(),
            ..Default::default()
        };
        let tok = sign(secret, c.clone(), now, Duration::from_secs(3600)).unwrap();
        assert!(!tok.contains(['/', '+', '=', ' ']), "sign: {tok}");
        let got = verify(secret, &tok, now + chrono::Duration::minutes(30)).unwrap();
        assert_eq!(
            (
                got.end_user.as_str(),
                got.change.as_str(),
                got.process.as_str()
            ),
            ("alice", "C1", "P1")
        );
        assert_eq!(
            verify(secret, &tok, now + chrono::Duration::hours(2)),
            Err(Error::Expired)
        );
        assert_eq!(verify(b"other", &tok, now), Err(Error::Invalid));
        let (payload, sig) = tok.split_once('.').unwrap();
        let forged = format!("{}xx.{sig}", &payload[..payload.len() - 2]);
        assert_eq!(verify(secret, &forged, now), Err(Error::Invalid));
        assert!(
            sign(b"", c, now, Duration::from_secs(3600)).is_err(),
            "no secret must refuse"
        );
    }

    /// The payload is the JSON Go writes: the fields in order, the empty ones left out.
    #[test]
    fn payload_as_go_writes_it() {
        let now = DateTime::from_timestamp(100, 0).unwrap();
        let tok = sign(
            b"k",
            Claims {
                subject: "system:x".into(),
                change: "C".into(),
                ..Default::default()
            },
            now,
            Duration::from_secs(5),
        )
        .unwrap();
        let body = ENC.decode(tok.split_once('.').unwrap().0).unwrap();
        assert_eq!(
            String::from_utf8(body).unwrap(),
            r#"{"sub":"system:x","chg":"C","exp":105}"#
        );
    }
}
