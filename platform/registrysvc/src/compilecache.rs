//! The compiled methodologies of the registry (ADR 0072) and the one activation of a
//! stored version (ADR 0126 step 6).

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicI64, Ordering};

use goap_ctx::Ctx;
use goap_def::Domain;
use goap_methodology::{Compiled, Methodology};
use sha2::{Digest, Sha256};

use crate::error::{Error, Result, SuspendedError};
use crate::service::{Service, resolve};
use crate::store::{Record, STATUS_PUBLISHED, key};

/// maxCompiled bounds the compile cache: the latest published version of every methodology, with room for the
/// versions in use. When it is full the cache starts over (a compile is only a cost, never a result to keep).
const MAX_COMPILED: usize = 256;

/// compileKey identifies one compilation: the definition (name, version and a hash of its content: a draft is
/// replaced in place) and the types it was compiled against (the published domains in force, name@version each).
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct CompileKey {
    name: String,
    version: String,
    content: String,
    types: String,
}

/// The result of one compilation, kept as is (the refusal too).
type CompileEntry = std::result::Result<Arc<Compiled>, Arc<dyn std::error::Error + Send + Sync>>;

/// compileCache holds the compiled methodologies of the registry (ADR 0072): Methodology and List are called on the
/// hot paths of the engine and a compile is the expensive part. The key makes a stale hit impossible; the service
/// also empties it on every write it publishes. Safe for concurrent use.
#[derive(Default)]
pub(crate) struct CompileCache {
    entries: parking_lot::Mutex<Option<HashMap<CompileKey, CompileEntry>>>,
    /// compiles counts the compilations done on a miss (a test seam: a second read must not add one).
    pub(crate) compiles: AtomicI64,
}

impl CompileCache {
    fn get(&self, k: &CompileKey) -> Option<CompileEntry> {
        self.entries.lock().as_ref().and_then(|m| m.get(k).cloned())
    }

    fn put(&self, k: CompileKey, e: CompileEntry) {
        let mut g = self.entries.lock();
        if g.as_ref().is_none_or(|m| m.len() >= MAX_COMPILED) {
            *g = Some(HashMap::new());
        }
        if let Some(m) = g.as_mut() {
            m.insert(k, e);
        }
    }

    pub(crate) fn clear(&self) {
        *self.entries.lock() = None;
    }

    /// The compilations done so far.
    #[cfg_attr(not(test), allow(dead_code))]
    pub(crate) fn count(&self) -> i64 {
        self.compiles.load(Ordering::SeqCst)
    }
}

/// typesStamp names the types in force: the published domains, name@version each, in name order (a published
/// version is immutable, a republish is a new version).
pub(crate) fn types_stamp(ds: &[Domain]) -> String {
    let mut parts: Vec<String> = ds.iter().map(|d| key(&d.name, &d.version)).collect();
    parts.sort();
    parts.join(",")
}

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

fn content_hash(m: &Methodology) -> String {
    match serde_json::to_vec(m) {
        Ok(b) => hex(&Sha256::digest(&b)),
        Err(_) => String::new(), // not cacheable under a shared key: the caller compiles anyway
    }
}

/// Activate builds the activable form of a methodology version from its definition and the domains it compiles
/// against (ADR 0126): the one compilation of a stored version, run by the registry and by its clients
/// (Client::methodology) with the same inputs. It is the refusal of the compiler, or of the type catalogue when the
/// domains do not make one together.
pub fn activate(m: &Methodology, ds: &[Domain]) -> Result<Compiled> {
    let cat = goap_typecat::new(ds)?;
    Ok(resolve(m, Arc::new(cat)).compile()?)
}

fn activate_entry(m: &Methodology, ds: &[Domain]) -> CompileEntry {
    let cat = goap_typecat::new(ds)
        .map_err(|e| Arc::new(e) as Arc<dyn std::error::Error + Send + Sync>)?;
    resolve(m, Arc::new(cat))
        .compile()
        .map(Arc::new)
        .map_err(|e| Arc::new(e) as Arc<dyn std::error::Error + Send + Sync>)
}

impl Service {
    /// CompileDomains are the domain versions a stored methodology version compiles against: the ones it pins, else
    /// the ones in force at its publication, else the ones in force (ADR 0113).
    pub async fn compile_domains(&self, ctx: &Ctx, r: &Record) -> Result<Vec<Domain>> {
        let in_force = self.domains_in_force(ctx).await?;
        self.domains_among(ctx, &r.methodology, r.published_at, in_force)
            .await
    }

    /// Methodology implements engine.MethodologyPort for in-process use: the version a reference names
    /// (domain::methodology_ref, ADR 0113), else the latest published one, compiled against the domain versions it
    /// pins once for as long as neither it nor those types change. A published version the domains in force break is
    /// suspended (ErrSuspended, ADR 0125 §3).
    pub async fn methodology(&self, ctx: &Ctx, r#ref: &str) -> Result<Arc<Compiled>> {
        let r = match self.record(ctx, r#ref).await {
            Ok(r) => r,
            Err(_) => return Err(Error::UnknownMethodology(r#ref.to_string())),
        };
        let in_force = self.domains_in_force(ctx).await?;
        let ds = self
            .domains_among(ctx, &r.methodology, r.published_at, in_force.clone())
            .await?;
        let mut res = self.compile_against(&r, &ds);
        let mut broken = res.as_ref().err().cloned();
        if broken.is_none() && r.status == STATUS_PUBLISHED {
            // suspended (ADR 0125 §3): the same compile when it pins what is in force
            broken = self.compile_against(&r, &in_force).err();
        }
        if let Some(cause) = broken {
            if r.status == STATUS_PUBLISHED {
                return Err(Error::Suspended(SuspendedError {
                    r#ref: key(&r.methodology.name, &r.methodology.version),
                    cause: cause.to_string(),
                    graph_invalid: false,
                }));
            }
            res = Err(cause);
        }
        res.map_err(Error::Compile)
    }

    /// compileAgainst compiles a stored version against domains, through the cache: the refusal of the compiler, or
    /// of the type catalogue when the domains do not make one together.
    pub(crate) fn compile_against(&self, r: &Record, ds: &[Domain]) -> CompileEntry {
        let k = CompileKey {
            name: r.methodology.name.clone(),
            version: r.methodology.version.clone(),
            content: content_hash(&r.methodology),
            types: types_stamp(ds),
        };
        if !k.content.is_empty()
            && let Some(e) = self.compiled.get(&k)
        {
            return e;
        }
        self.compiled.compiles.fetch_add(1, Ordering::SeqCst);
        let e = activate_entry(&r.methodology, ds);
        if !k.content.is_empty() {
            self.compiled.put(k, e.clone());
        }
        e
    }
}
