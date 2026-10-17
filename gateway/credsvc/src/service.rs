use std::sync::Arc;

use chrono::{TimeDelta, Utc};
use goap_ctx::Ctx;

use crate::store::{Session, Store};

/// The errors of the service and its stores.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A request the service does not accept (`ErrInvalid`: empty subject/password, too short): the whole message,
    /// `invalid: ...`.
    #[error("{0}")]
    Invalid(String),
    /// Register when the subject already has a credential (`ErrExists`).
    #[error("credential already exists")]
    Exists,
    /// A subject with no credential, a session that does not exist (`ErrNotFound`).
    #[error("credential not found")]
    NotFound,
    #[error(transparent)]
    Bcrypt(#[from] bcrypt::BcryptError),
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
    /// A stored time that does not parse.
    #[error("{0}")]
    Time(String),
    #[error("{0}")]
    Other(String),
}

impl Error {
    /// `errors.Is(err, ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_))
    }
    /// `errors.Is(err, ErrExists)`.
    pub fn is_exists(&self) -> bool {
        matches!(self, Error::Exists)
    }
    /// `errors.Is(err, ErrNotFound)`.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound)
    }
}

/// MinPasswordLen is the shortest password Register/SetPassword accepts.
pub const MIN_PASSWORD_LEN: usize = 8;

/// Go's `bcrypt.DefaultCost`.
const COST: u32 = 10;

fn validate(subject: &str, password: &str) -> Result<(), Error> {
    if subject.is_empty() {
        return Err(Error::Invalid("invalid: subject required".into()));
    }
    if password.len() < MIN_PASSWORD_LEN {
        return Err(Error::Invalid(format!(
            "invalid: password must be at least {MIN_PASSWORD_LEN} characters"
        )));
    }
    Ok(())
}

/// The bcrypt hash of a password, as Go's `bcrypt.GenerateFromPassword` writes it (`$2a$`), on the blocking pool.
async fn hash(password: &str) -> Result<String, Error> {
    let p = password.to_string();
    tokio::task::spawn_blocking(move || {
        bcrypt::non_truncating_hash_with_result(p, COST)
            .map(|h| h.format_for_version(bcrypt::Version::TwoA))
    })
    .await
    .map_err(|e| Error::Other(e.to_string()))?
    .map_err(Error::from)
}

/// Service registers and verifies local credentials.
#[derive(Clone)]
pub struct Service {
    pub store: Arc<dyn Store>,
}

impl Service {
    pub fn new(store: Arc<dyn Store>) -> Self {
        Service { store }
    }

    /// Register creates the credential of a new subject. [`Error::Exists`] if one is already there.
    pub async fn register(&self, ctx: &Ctx, subject: &str, password: &str) -> Result<(), Error> {
        validate(subject, password)?;
        let h = hash(password).await?;
        self.store.create(ctx, subject, &h).await
    }

    /// Verify reports whether password matches the stored hash of subject. A subject with no credential (or a wrong
    /// password) is simply "no match" — the caller cannot tell which, so a login attempt gives no signal about which
    /// subjects have registered.
    pub async fn verify(&self, ctx: &Ctx, subject: &str, password: &str) -> Result<bool, Error> {
        if subject.is_empty() || password.is_empty() {
            return Ok(false);
        }
        let h = match self.store.hash(ctx, subject).await {
            Ok(h) => h,
            Err(Error::NotFound) => return Ok(false),
            Err(e) => return Err(e),
        };
        let p = password.to_string();
        tokio::task::spawn_blocking(move || bcrypt::non_truncating_verify(p, &h).unwrap_or(false))
            .await
            .map_err(|e| Error::Other(e.to_string()))
    }

    /// SetPassword replaces the password of a subject that already has a credential.
    pub async fn set_password(
        &self,
        ctx: &Ctx,
        subject: &str,
        password: &str,
    ) -> Result<(), Error> {
        validate(subject, password)?;
        let h = hash(password).await?;
        self.store.set(ctx, subject, &h).await?;
        // a new password ends every session signed in with the old one (ADR 0045)
        self.end_sessions(ctx, subject).await
    }

    /// Exists reports whether a subject already has a local credential.
    pub async fn exists(&self, ctx: &Ctx, subject: &str) -> Result<bool, Error> {
        if subject.is_empty() {
            return Ok(false);
        }
        self.store.exists(ctx, subject).await
    }

    /// StartSession opens a sign-in session of a subject (ADR 0045), valid max_age, and returns its id: every token
    /// issued from this sign-in carries it, and stops being accepted once the session ends.
    pub async fn start_session(
        &self,
        ctx: &Ctx,
        subject: &str,
        max_age: std::time::Duration,
    ) -> Result<String, Error> {
        if subject.is_empty() || max_age.is_zero() {
            return Err(Error::Invalid(
                "invalid: subject and session duration required".into(),
            ));
        }
        let id: String = uuid::Uuid::new_v4()
            .as_bytes()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        let now = Utc::now();
        let max_age = TimeDelta::from_std(max_age).unwrap_or(TimeDelta::MAX);
        let sess = Session {
            id: id.clone(),
            subject: subject.to_string(),
            created: now,
            expires: now.checked_add_signed(max_age).unwrap_or(now),
            revoked: None,
        };
        // sessions expired for a day are forgotten as new ones open
        self.store
            .create_session(ctx, sess, now - TimeDelta::hours(24))
            .await?;
        Ok(id)
    }

    /// SessionActive reports whether a session of subject still accepts its tokens: it exists, belongs to subject,
    /// was not ended and has not expired.
    pub async fn session_active(&self, ctx: &Ctx, id: &str, subject: &str) -> Result<bool, Error> {
        if id.is_empty() {
            return Ok(false);
        }
        match self.store.session(ctx, id).await {
            Ok(s) => Ok(s.subject == subject && s.active(Utc::now())),
            Err(Error::NotFound) => Ok(false),
            Err(e) => Err(e),
        }
    }

    /// EndSession ends a session (signing out): its tokens are refused from now on.
    pub async fn end_session(&self, ctx: &Ctx, id: &str) -> Result<(), Error> {
        if id.is_empty() {
            return Ok(());
        }
        self.store.revoke_session(ctx, id, Utc::now()).await
    }

    /// EndSessions ends every session of a subject (signing out everywhere, a new password).
    pub async fn end_sessions(&self, ctx: &Ctx, subject: &str) -> Result<(), Error> {
        if subject.is_empty() {
            return Ok(());
        }
        self.store.revoke_sessions(ctx, subject, Utc::now()).await
    }
}
