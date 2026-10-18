//! The engine seam: what a JavaScript engine must offer the DSL, independent of the engine (boa today).

use std::{
    fmt,
    sync::{
        Arc, LazyLock,
        atomic::{AtomicBool, Ordering},
    },
    time::Instant,
};

use parking_lot::{Mutex, RwLock};
use serde_json::Value;
use sha2::{Digest, Sha256};

use crate::boa::BoaEngine;

/// The source of a script and the hash it is cached by (SHA-256 of the code).
#[derive(Clone)]
pub struct ScriptSource {
    code: Arc<str>,
    hash: [u8; 32],
}

impl ScriptSource {
    /// A source, hashed once.
    pub fn new(code: impl Into<Arc<str>>) -> Self {
        let code = code.into();
        let hash = Sha256::digest(code.as_bytes()).into();
        ScriptSource { code, hash }
    }

    /// The code.
    pub fn code(&self) -> &str {
        &self.code
    }

    /// The SHA-256 of the code: the key of the engine's cache of compiled scripts.
    pub fn hash(&self) -> &[u8; 32] {
        &self.hash
    }
}

impl fmt::Debug for ScriptSource {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let short: String = self.hash[..6].iter().map(|b| format!("{b:02x}")).collect();
        f.debug_struct("ScriptSource")
            .field("hash", &short)
            .field("len", &self.code.len())
            .finish()
    }
}

/// The `ctx` object a script sees, as an engine sees it: named methods called with JSON values. Each usage of the
/// DSL (action, algorithm, adapter) has its own; the engine exposes [`ScriptHost::methods`] as the functions of the
/// global `ctx` and routes `console.log` / `console.warn` to [`ScriptHost::log`].
///
/// The methods are called on the script's thread, one at a time; an implementation keeps its state behind interior
/// mutability (the run reads it once the script is over).
pub trait ScriptHost: Send + Sync {
    /// The names of the methods of `ctx` (camelCase: `impactNode`, `changeImpacts`...).
    fn methods(&self) -> &'static [&'static str];

    /// Calls a method. A missing or `undefined` argument is `null`. `Ok(None)` is `undefined`; `Err(message)` is
    /// thrown in the script as an `Error` with that message (a script may catch it).
    fn call(&self, method: &str, args: &[Value]) -> Result<Option<Value>, String>;

    /// A line of the console of the script (`info` for `console.log`, `warn` for `console.warn`), already
    /// formatted (Go's `fmt.Sprint` of the arguments, [`crate::sprint`]).
    fn log(&self, level: &str, msg: String);
}

/// Why a run was stopped, shared by the run and the engine: set when the run's context is done; the engine stops the
/// script as soon as it sees it (and at its deadline in any case).
#[derive(Debug, Default)]
pub struct Interrupt {
    set: AtomicBool,
    reason: Mutex<String>,
    deadline: Option<Instant>,
}

impl Interrupt {
    /// An interrupt that fires at `deadline` (when given) or when [`Interrupt::set`] is called.
    pub fn new(deadline: Option<Instant>) -> Self {
        Interrupt {
            deadline,
            ..Default::default()
        }
    }

    /// Stops the script, with the cause (`context deadline exceeded`, `context canceled`).
    pub fn set(&self, reason: impl Into<String>) {
        let mut r = self.reason.lock();
        if !self.set.load(Ordering::SeqCst) {
            *r = reason.into();
            self.set.store(true, Ordering::SeqCst);
        }
    }

    /// Whether the script must stop now.
    pub fn is_set(&self) -> bool {
        self.set.load(Ordering::Relaxed) || self.deadline.is_some_and(|d| Instant::now() >= d)
    }

    /// The cause of the interruption.
    pub fn reason(&self) -> String {
        if self.set.load(Ordering::SeqCst) {
            self.reason.lock().clone()
        } else {
            "context deadline exceeded".to_string()
        }
    }
}

/// A failure of a script as an engine reports it.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum ScriptError {
    /// The code does not compile, threw, or exceeded a limit of the engine (`Error: boom`, `SyntaxError: ...`).
    #[error("{0}")]
    Failed(String),
    /// The [`Interrupt`] stopped it; the cause.
    #[error("script interrupted: {0}")]
    Interrupted(String),
}

/// A JavaScript engine for the DSL. An implementation compiles a source once and caches it by
/// [`ScriptSource::hash`]; it runs each execution isolated from the others as far as it can (see [`BoaEngine`]).
pub trait ScriptEngine: Send + Sync {
    /// The name of the engine (`boa`).
    fn name(&self) -> &'static str;

    /// Checks that a source compiles, without running it.
    fn check(&self, source: &ScriptSource) -> Result<(), ScriptError>;

    /// Runs a script synchronously on the calling thread (a blocking thread of the run): the global `ctx` is `host`,
    /// `console.log` / `console.warn` log to it, the code runs, then, when it declares a function `run`, `run(ctx)` is
    /// called. The result is what `run` returned (`None`: no `run`, or it returned `undefined` or `null`), as JSON (an
    /// integral number is an integer). The engine stops the script once `interrupt` is set.
    fn run(
        &self,
        source: &ScriptSource,
        host: Arc<dyn ScriptHost>,
        interrupt: &Interrupt,
    ) -> Result<Option<Value>, ScriptError>;
}

static ENGINE: LazyLock<RwLock<Arc<dyn ScriptEngine>>> =
    LazyLock::new(|| RwLock::new(Arc::new(BoaEngine::default())));

/// The engine the runs of the process use ([`BoaEngine`] unless [`set_engine`] replaced it).
pub fn engine() -> Arc<dyn ScriptEngine> {
    ENGINE.read().clone()
}

/// Replaces the engine of the process (for the runs started after).
pub fn set_engine(e: Arc<dyn ScriptEngine>) {
    *ENGINE.write() = e;
}
