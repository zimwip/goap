use std::sync::Arc;

use goap_ctx::Ctx;
use serde_json::Value;

use crate::store::{Prefs, Store, go_json};

/// The errors of the service and its stores.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A document the service does not accept (`ErrInvalid`): the whole message, `invalid: ...`.
    #[error("{0}")]
    Invalid(String),
    /// The caller is not identified (`ErrAnonymous`): preferences belong to a declared user.
    #[error("preferences need an identified user")]
    Anonymous,
    /// A stored document that does not decode.
    #[error("stored preferences: {0}")]
    Stored(#[source] serde_json::Error),
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
}

impl Error {
    /// `errors.Is(err, ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_))
    }

    /// `errors.Is(err, ErrAnonymous)`.
    pub fn is_anonymous(&self) -> bool {
        matches!(self, Error::Anonymous)
    }
}

/// MaxDocumentBytes bounds the JSON encoding of a subject's document: the service keeps it opaque, so the only thing
/// it protects is its own storage.
pub const MAX_DOCUMENT_BYTES: usize = 64 << 10;

/// check refuses a document that is not a JSON object the service can keep: larger than MAX_DOCUMENT_BYTES. What the
/// keys mean, and which values they accept, is the business of the interface reading them
/// (web/src/lib/stores/preferences.svelte.ts), never of this service (ADR 0068).
fn check(doc: &Prefs) -> Result<(), Error> {
    let n = go_json(doc).len();
    if n > MAX_DOCUMENT_BYTES {
        return Err(Error::Invalid(format!(
            "invalid: the document is {n} bytes, at most {MAX_DOCUMENT_BYTES}"
        )));
    }
    Ok(())
}

/// Service reads and writes the preferences of a subject.
#[derive(Clone)]
pub struct Service {
    pub store: Arc<dyn Store>,
}

impl Service {
    pub fn new(store: Arc<dyn Store>) -> Self {
        Service { store }
    }

    /// Get returns the preferences of a subject.
    pub async fn get(&self, ctx: &Ctx, subject: &str) -> Result<Prefs, Error> {
        if subject.is_empty() {
            return Err(Error::Anonymous);
        }
        self.store.get(ctx, subject).await
    }

    /// Set merges a patch into the document of a subject: a null value clears the key. Any key is kept; the result
    /// must stay a JSON object of at most MAX_DOCUMENT_BYTES.
    pub async fn set(&self, ctx: &Ctx, subject: &str, patch: Prefs) -> Result<Prefs, Error> {
        if subject.is_empty() {
            return Err(Error::Anonymous);
        }
        self.store
            .update(
                ctx,
                subject,
                Box::new(move |cur: Prefs| {
                    let mut out = cur;
                    for (k, v) in patch {
                        if v == Value::Null {
                            out.remove(&k);
                        } else {
                            out.insert(k, v);
                        }
                    }
                    check(&out)?;
                    Ok(out)
                }),
            )
            .await
    }

    /// Reset forgets the preferences of a subject.
    pub async fn reset(&self, ctx: &Ctx, subject: &str) -> Result<(), Error> {
        if subject.is_empty() {
            return Err(Error::Anonymous);
        }
        self.store.delete(ctx, subject).await
    }
}
