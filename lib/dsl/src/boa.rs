//! The [`ScriptEngine`] on `boa_engine` (pure Rust).
//!
//! # Compilation and cache
//!
//! A boa script is bound to the realm it was parsed in, and a realm cannot be shared between threads; so each
//! blocking thread that runs scripts keeps its own cache (bounded, least recently used out), keyed by the SHA-256 of
//! the source. An entry is a boa context of its own (a realm per script: two scripts never see each other's globals)
//! in which the code was parsed and compiled once, wrapped in a function:
//!
//! ```js
//! function __goap_main() {<code>
//! ;return typeof run === "function" ? run(globalThis.ctx) : undefined;
//! }
//! ```
//!
//! A run sets the globals `ctx` (the [`ScriptHost`] of the run) and calls `__goap_main()`: the declarations of the
//! code are local to each run, so a script runs again in the same realm without parsing anything; what a run leaves
//! on the global object (an undeclared assignment, a modified prototype) is seen by the next run of the same code
//! only. The header sits on the line of the code's first line, so the positions of errors keep their lines.
//!
//! # Limits and interruption
//!
//! The script is evaluated with boa's budgeted evaluation, polled by hand on the blocking thread: every
//! [`BoaEngine::budget`] "cycles" it yields and the [`Interrupt`] is checked, so a deadline stops ordinary JavaScript
//! (loops, recursion, calls between JavaScript functions) within microseconds; the context is then dropped (not put
//! back in the cache). Code running inside a native callback (the function given to `Array.prototype.forEach`, a
//! getter called by the engine) is not budgeted: there the loop iteration limit ([`BoaEngine::loop_iteration_limit`],
//! per loop) and the recursion limit stop it.

use std::{
    cell::RefCell,
    collections::HashMap,
    pin::pin,
    sync::Arc,
    task::{Context as TaskContext, Poll, Waker},
};

use boa_engine::{
    Context, JsError, JsNativeError, JsResult, JsString, JsValue, NativeFunction, Script, Source,
    js_string, object::ObjectInitializer, property::Attribute,
};
use serde_json::Value;

use crate::{
    engine::{Interrupt, ScriptEngine, ScriptError, ScriptHost, ScriptSource},
    gofmt::sprint,
};

/// The JavaScript engine of the DSL on `boa_engine`.
#[derive(Debug, Clone)]
pub struct BoaEngine {
    /// The "cycles" of the VM between two checks of the interrupt.
    pub budget: u32,
    /// The iterations of one loop before it throws (`u64::MAX`: no limit).
    pub loop_iteration_limit: u64,
    /// The depth of the calls before it throws.
    pub recursion_limit: usize,
    /// The compiled scripts each thread keeps.
    pub cache_size: usize,
}

impl Default for BoaEngine {
    fn default() -> Self {
        BoaEngine {
            budget: 256,
            loop_iteration_limit: 100_000_000,
            recursion_limit: 512,
            cache_size: 32,
        }
    }
}

/// A compiled script: its own context, where `__goap_main` is defined, and the call of it.
struct Compiled {
    context: Context,
    invoke: Script,
    used: u64,
}

#[derive(Default)]
struct Cache {
    entries: HashMap<[u8; 32], Compiled>,
    tick: u64,
}

impl Cache {
    fn take(&mut self, key: &[u8; 32]) -> Option<Compiled> {
        self.entries.remove(key)
    }

    fn put(&mut self, key: [u8; 32], mut c: Compiled, cap: usize) {
        self.tick += 1;
        c.used = self.tick;
        self.entries.insert(key, c);
        while self.entries.len() > cap.max(1) {
            let Some(oldest) = self
                .entries
                .iter()
                .min_by_key(|(_, c)| c.used)
                .map(|(k, _)| *k)
            else {
                break;
            };
            self.entries.remove(&oldest);
        }
    }
}

thread_local! {
    // The cache holds boa contexts, which free their objects into the garbage collector of the thread (a
    // thread-local of boa_gc): that collector must outlive the cache. Thread-locals are destroyed in the reverse
    // order of their initialisation, so the collector is initialised first, by the initialiser of the cache.
    static CACHE: RefCell<Cache> = {
        boa_engine::gc::force_collect();
        RefCell::new(Cache::default())
    };
}

/// The host of the run in progress in a context.
struct HostSlot(Arc<dyn ScriptHost>);

fn host_of(context: &Context) -> JsResult<Arc<dyn ScriptHost>> {
    context
        .get_data::<HostSlot>()
        .map(|s| s.0.clone())
        .ok_or_else(|| {
            JsNativeError::error()
                .with_message("ctx is not available now")
                .into()
        })
}

/// A method of `ctx`: the arguments as JSON, the host's answer back, its failure thrown.
fn dispatch(
    _this: &JsValue,
    args: &[JsValue],
    method: &&'static str,
    context: &mut Context,
) -> JsResult<JsValue> {
    let host = host_of(context)?;
    let mut values = Vec::with_capacity(args.len());
    for a in args {
        values.push(to_json(a, context)?.unwrap_or(Value::Null));
    }
    match host.call(method, &values) {
        Ok(Some(v)) => JsValue::from_json(&v, context),
        Ok(None) => Ok(JsValue::undefined()),
        Err(msg) => Err(JsNativeError::error().with_message(msg).into()),
    }
}

/// `console.log` / `console.warn`: Go's `fmt.Sprint` of the arguments.
fn console(
    _this: &JsValue,
    args: &[JsValue],
    level: &&'static str,
    context: &mut Context,
) -> JsResult<JsValue> {
    let host = host_of(context)?;
    let mut values = Vec::with_capacity(args.len());
    for a in args {
        values.push(match a.as_callable() {
            Some(_) => Value::String(a.to_string(context)?.to_std_string_escaped()),
            None => to_json(a, context)?.unwrap_or(Value::Null),
        });
    }
    host.log(level, sprint(&values));
    Ok(JsValue::undefined())
}

/// A JavaScript value as JSON, the way `JSON.stringify` writes it (`None`: undefined, a function, a symbol).
fn to_json(v: &JsValue, context: &mut Context) -> JsResult<Option<Value>> {
    if v.is_undefined() {
        return Ok(None);
    }
    if v.is_null() {
        return Ok(Some(Value::Null));
    }
    if let Some(s) = v.as_string() {
        return Ok(Some(Value::String(s.to_std_string_escaped())));
    }
    if let Some(b) = v.as_boolean() {
        return Ok(Some(Value::Bool(b)));
    }
    let json = context.intrinsics().objects().json();
    let stringify = json.get(js_string!("stringify"), context)?;
    let Some(stringify) = stringify.as_callable() else {
        return Err(JsNativeError::typ()
            .with_message("JSON.stringify is not a function")
            .into());
    };
    let text = stringify.call(&JsValue::undefined(), std::slice::from_ref(v), context)?;
    let Some(text) = text.as_string() else {
        return Ok(None);
    };
    serde_json::from_str(&text.to_std_string_escaped())
        .map(Some)
        .map_err(|e| JsNativeError::typ().with_message(e.to_string()).into())
}

/// The message of a JavaScript error: `<name>: <message>` for an error object, its string otherwise.
fn message_of(e: JsError, context: &mut Context) -> String {
    match e.try_native(context) {
        Ok(native) => {
            let msg = native.message().trim();
            if msg.is_empty() {
                native.kind().to_string()
            } else {
                format!("{}: {msg}", native.kind())
            }
        }
        Err(_) => match e.as_opaque().cloned() {
            Some(v) => v
                .to_string(context)
                .map(|s| s.to_std_string_escaped())
                .unwrap_or_else(|_| e.to_string()),
            None => e.to_string(),
        },
    }
}

impl BoaEngine {
    fn compile(&self, source: &ScriptSource) -> Result<Compiled, ScriptError> {
        let mut context = Context::default();
        let failed = |e: JsError, c: &mut Context| ScriptError::Failed(message_of(e, c));
        let mut console_obj = ObjectInitializer::new(&mut context);
        console_obj
            .function(
                NativeFunction::from_copy_closure_with_captures(console, "info"),
                js_string!("log"),
                0,
            )
            .function(
                NativeFunction::from_copy_closure_with_captures(console, "warn"),
                js_string!("warn"),
                0,
            );
        let console_obj = console_obj.build();
        if let Err(e) =
            context.register_global_property(js_string!("console"), console_obj, Attribute::all())
        {
            return Err(failed(e, &mut context));
        }
        let wrapped = format!(
            "function __goap_main() {{{}\n;return typeof run === \"function\" ? run(globalThis.ctx) : undefined;\n}}",
            source.code()
        );
        let defined = Script::parse(Source::from_bytes(wrapped.as_bytes()), None, &mut context)
            .and_then(|s| s.evaluate(&mut context));
        if let Err(e) = defined {
            return Err(failed(e, &mut context));
        }
        match Script::parse(Source::from_bytes("__goap_main()"), None, &mut context) {
            Ok(invoke) => Ok(Compiled {
                context,
                invoke,
                used: 0,
            }),
            Err(e) => Err(failed(e, &mut context)),
        }
    }

    /// Runs a compiled script; `None` when it was interrupted (its context is then unusable).
    fn evaluate(
        &self,
        c: &mut Compiled,
        host: Arc<dyn ScriptHost>,
        interrupt: &Interrupt,
    ) -> Option<Result<Option<Value>, String>> {
        let context = &mut c.context;
        let limits = context.runtime_limits_mut();
        limits.set_loop_iteration_limit(self.loop_iteration_limit);
        limits.set_recursion_limit(self.recursion_limit);
        let mut obj = ObjectInitializer::new(context);
        for m in host.methods() {
            obj.function(
                NativeFunction::from_copy_closure_with_captures(dispatch, *m),
                JsString::from(*m),
                0,
            );
        }
        let obj = obj.build();
        context.insert_data(HostSlot(host));
        let global = context.global_object();
        if let Err(e) = global.set(js_string!("ctx"), obj, false, context) {
            context.remove_data::<HostSlot>();
            return Some(Err(message_of(e, context)));
        }
        let completion = {
            let mut fut = pin!(c.invoke.evaluate_async_with_budget(context, self.budget));
            let mut cx = TaskContext::from_waker(Waker::noop());
            loop {
                if interrupt.is_set() {
                    break None;
                }
                if let Poll::Ready(r) = fut.as_mut().poll(&mut cx) {
                    break Some(r);
                }
            }
        };
        let context = &mut c.context;
        let out = match completion {
            None => None,
            Some(Ok(v)) => Some(to_json(&v, context).map_err(|e| message_of(e, context))),
            Some(Err(e)) => Some(Err(message_of(e, context))),
        };
        context.remove_data::<HostSlot>();
        if out.is_some() {
            let _ = context.global_object().set(
                js_string!("ctx"),
                JsValue::undefined(),
                false,
                context,
            );
        }
        out
    }
}

impl ScriptEngine for BoaEngine {
    fn name(&self) -> &'static str {
        "boa"
    }

    fn check(&self, source: &ScriptSource) -> Result<(), ScriptError> {
        if CACHE.with(|c| c.borrow().entries.contains_key(source.hash())) {
            return Ok(());
        }
        let compiled = self.compile(source)?;
        CACHE.with(|c| {
            c.borrow_mut()
                .put(*source.hash(), compiled, self.cache_size)
        });
        Ok(())
    }

    fn run(
        &self,
        source: &ScriptSource,
        host: Arc<dyn ScriptHost>,
        interrupt: &Interrupt,
    ) -> Result<Option<Value>, ScriptError> {
        if interrupt.is_set() {
            return Err(ScriptError::Interrupted(interrupt.reason()));
        }
        let mut compiled = match CACHE.with(|c| c.borrow_mut().take(source.hash())) {
            Some(c) => c,
            None => self.compile(source)?,
        };
        match self.evaluate(&mut compiled, host, interrupt) {
            None => Err(ScriptError::Interrupted(interrupt.reason())),
            Some(r) => {
                CACHE.with(|c| {
                    c.borrow_mut()
                        .put(*source.hash(), compiled, self.cache_size)
                });
                match r {
                    Ok(Some(Value::Null)) | Ok(None) => Ok(None),
                    Ok(v) => Ok(v),
                    Err(msg) => Err(ScriptError::Failed(msg)),
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use parking_lot::Mutex;

    use super::*;

    struct Recorder {
        calls: Mutex<Vec<(String, Vec<Value>)>>,
        logs: Mutex<Vec<String>>,
    }

    impl ScriptHost for Recorder {
        fn methods(&self) -> &'static [&'static str] {
            &["echo", "boom"]
        }
        fn call(&self, method: &str, args: &[Value]) -> Result<Option<Value>, String> {
            self.calls.lock().push((method.to_string(), args.to_vec()));
            match method {
                "echo" => Ok(Some(args.first().cloned().unwrap_or(Value::Null))),
                _ => Err("no such file".into()),
            }
        }
        fn log(&self, _level: &str, msg: String) {
            self.logs.lock().push(msg);
        }
    }

    fn recorder() -> Arc<Recorder> {
        Arc::new(Recorder {
            calls: Mutex::default(),
            logs: Mutex::default(),
        })
    }

    #[test]
    fn runs_again_from_the_cache_with_fresh_declarations() {
        let e = BoaEngine::default();
        let src = ScriptSource::new(
            "const n = ctx.echo(41) + 1;\nfunction run(ctx) { return {n: n, f: 1.5}; }",
        );
        for _ in 0..3 {
            let r = e.run(&src, recorder(), &Interrupt::new(None)).unwrap();
            assert_eq!(r, Some(serde_json::json!({"n": 42, "f": 1.5})));
        }
        assert!(CACHE.with(|c| c.borrow().entries.contains_key(src.hash())));
    }

    #[test]
    fn errors_and_console() {
        let e = BoaEngine::default();
        let h = recorder();
        let err = e
            .run(&ScriptSource::new("try { ctx.boom() } catch (x) { console.log(String(x), 1, 2) }\nthrow new TypeError(\"bad\")"), h.clone(), &Interrupt::new(None))
            .unwrap_err();
        assert_eq!(err, ScriptError::Failed("TypeError: bad".into()));
        assert_eq!(h.logs.lock().as_slice(), ["Error: no such file1 2"]);
        let err = e
            .run(
                &ScriptSource::new("throw 'plain'"),
                recorder(),
                &Interrupt::new(None),
            )
            .unwrap_err();
        assert_eq!(err, ScriptError::Failed("plain".into()));
        let err = e.check(&ScriptSource::new("function (")).unwrap_err();
        assert!(err.to_string().starts_with("SyntaxError"), "{err}");
    }

    #[test]
    fn an_interrupt_stops_a_loop() {
        let e = BoaEngine::default();
        let i = Interrupt::new(Some(
            std::time::Instant::now() + std::time::Duration::from_millis(50),
        ));
        let err = e
            .run(&ScriptSource::new("while (true) {}"), recorder(), &i)
            .unwrap_err();
        assert_eq!(
            err,
            ScriptError::Interrupted("context deadline exceeded".into())
        );
        let e = BoaEngine {
            loop_iteration_limit: 1000,
            ..Default::default()
        };
        let err = e
            .run(
                &ScriptSource::new("[1].forEach(() => { while (true) {} })"),
                recorder(),
                &Interrupt::new(None),
            )
            .unwrap_err();
        assert!(err.to_string().contains("RuntimeLimit"), "{err}");
    }
}
