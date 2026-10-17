//! The adapter usage (ADR 0019) implements the tools of an MCP with the operations of a connector. The script is the
//! body of function (ctx): ctx.tool() is the tool called, ctx.args() its arguments, ctx.param(name) a parameter of the
//! organisation's instance (secrets are never readable), ctx.call(operation, args) calls an operation the connector
//! exposes and returns its result (a failure throws), ctx.operations() lists them. The script returns the result of
//! the tool, or rejects with ctx.fail(message) / by throwing.

use std::{fmt, sync::Arc, time::Duration};

use async_trait::async_trait;
use goap_algo::Bound;
use parking_lot::Mutex;
use serde_json::{Map, Value};

use crate::{
    Error,
    algo::{Common, common_call, js_body},
    bridge::{Bridge, HostReply, HostRequest, unexpected},
    dsl::LogLine,
    engine::ScriptHost,
    run::drive,
    values::{arg_map, arg_str, to_value},
};

/// AdapterTimeout bounds one execution of an adapter, connector calls included.
pub const ADAPTER_TIMEOUT: Duration = Duration::from_secs(30);
/// MaxAdapterCalls bounds the connector calls of one execution.
pub const MAX_ADAPTER_CALLS: usize = 32;

/// Runs a connector operation: the hub closes it over the configuration and the secrets (Go's `AdapterInput.Call`).
#[async_trait]
pub trait OperationCall: Send + Sync {
    async fn call(
        &self,
        ctx: &goap_ctx::Ctx,
        operation: &str,
        args: Map<String, Value>,
    ) -> Result<Map<String, Value>, Error>;
}

/// AdapterInput is what an adapter is run on.
#[derive(Clone, Default)]
pub struct AdapterInput {
    pub tool: String,
    pub args: Map<String, Value>,
    /// Operations the connector exposes; ctx.call refuses any other.
    pub operations: Vec<String>,
    /// Call runs a connector operation.
    pub call: Option<Arc<dyn OperationCall>>,
}

impl fmt::Debug for AdapterInput {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("AdapterInput")
            .field("tool", &self.tool)
            .field("args", &self.args)
            .field("operations", &self.operations)
            .field("call", &self.call.is_some())
            .finish()
    }
}

/// AdapterOutcome is the result of an adapter.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct AdapterOutcome {
    /// Result is the result of the tool (`None`: the script returned none).
    pub result: Option<Map<String, Value>>,
    /// Failures are the reasons the adapter rejected; empty: accepted.
    pub failures: Vec<String>,
    pub logs: Vec<LogLine>,
    /// Calls is the number of connector calls made.
    pub calls: usize,
}

impl AdapterOutcome {
    /// OK tells whether the adapter accepted.
    pub fn ok(&self) -> bool {
        self.failures.is_empty()
    }
}

const ADAPTER_METHODS: &[&str] = &[
    "param",
    "instance",
    "algorithm",
    "tool",
    "args",
    "operations",
    "call",
    "fail",
    "log",
    "warn",
];

/// AdapterCtx is the context of an adapter script.
pub struct AdapterCtx {
    b: Bound,
    tool: String,
    args: Map<String, Value>,
    operations: Vec<String>,
    connected: bool,
    bridge: Bridge,
    common: Mutex<Common>,
    calls: Mutex<usize>,
}

impl AdapterCtx {
    /// Tool is the name of the MCP tool being called.
    pub fn tool(&self) -> &str {
        &self.tool
    }

    /// Args are the arguments of the tool call.
    pub fn args(&self) -> &Map<String, Value> {
        &self.args
    }

    /// Operations lists the operations the connector exposes.
    pub fn operations(&self) -> &[String] {
        &self.operations
    }

    /// Call runs an operation of the connector and returns its result. It fails on an operation the connector does
    /// not expose, when the call limit is exceeded and when the connector fails.
    fn call_operation(
        &self,
        operation: &str,
        args: Option<Map<String, Value>>,
    ) -> Result<Map<String, Value>, Error> {
        if !self.operations.iter().any(|o| o == operation) {
            return Err(Error::host(format!(
                "the connector exposes no operation {operation:?} (available: {})",
                self.operations.join(", ")
            )));
        }
        {
            let mut calls = self.calls.lock();
            if *calls >= MAX_ADAPTER_CALLS {
                return Err(Error::host(format!(
                    "more than {MAX_ADAPTER_CALLS} connector calls in one execution"
                )));
            }
            *calls += 1;
        }
        if !self.connected {
            return Err(Error::host("no connector to call"));
        }
        let req = HostRequest::Operation {
            operation: operation.to_string(),
            args: args.unwrap_or_default(),
        };
        match self.bridge.request(req)? {
            HostReply::Map(m) => Ok(m),
            r => Err(unexpected(r)),
        }
    }
}

impl ScriptHost for AdapterCtx {
    fn methods(&self) -> &'static [&'static str] {
        ADAPTER_METHODS
    }

    fn call(&self, method: &str, a: &[Value]) -> Result<Option<Value>, String> {
        if let Some(r) = common_call(&self.b, &self.common, method, a) {
            return Ok(r);
        }
        Ok(match method {
            "tool" => Some(self.tool.clone().into()),
            "args" => Some(Value::Object(self.args.clone())),
            "operations" => Some(to_value(&self.operations)),
            "call" => Some(Value::Object(
                self.call_operation(&arg_str(a, 0), arg_map(a, 1))
                    .map_err(|e| e.to_string())?,
            )),
            _ => return Err(format!("ctx has no method {method}")),
        })
    }

    fn log(&self, level: &str, msg: String) {
        self.common.lock().logf(level, msg);
    }
}

/// RunAdapter runs a bound adapter (its type is not used: the caller, the adapter definition, owns the usage). The
/// error is a failure of the script itself (does not compile, throws, times out, a connector call failed and was not
/// caught); a rejection by the script is in AdapterOutcome.failures. b.params must hold the non-secret parameter
/// values.
pub async fn run_adapter(
    ctx: &goap_ctx::Ctx,
    b: &Bound,
    input: AdapterInput,
) -> Result<AdapterOutcome, Error> {
    let (bridge, rx) = Bridge::new();
    let x = Arc::new(AdapterCtx {
        b: b.clone(),
        tool: input.tool,
        args: input.args,
        operations: input.operations,
        connected: input.call.is_some(),
        bridge,
        common: Mutex::default(),
        calls: Mutex::default(),
    });
    let call = input.call;
    let ran = drive(
        ctx,
        ADAPTER_TIMEOUT,
        &b.language,
        js_body(&b.code),
        x.clone(),
        rx,
        |rctx, req| {
            let call = call.clone();
            async move {
                match (req, call) {
                    (HostRequest::Operation { operation, args }, Some(c)) => {
                        Ok(HostReply::Map(c.call(&rctx, &operation, args).await?))
                    }
                    _ => Err(Error::host("no connector to call")),
                }
            }
        },
    )
    .await;
    let mut common = x.common.lock();
    // a JavaScript adapter returns the result of the tool: an object, or any other value wrapped
    let result = match &ran {
        Ok(Some(Value::Object(m))) => Some(m.clone()),
        Ok(Some(v)) => {
            let mut m = Map::new();
            m.insert("value".into(), v.clone());
            Some(m)
        }
        _ => None,
    };
    let out = AdapterOutcome {
        result,
        failures: std::mem::take(&mut common.failures),
        logs: std::mem::take(&mut common.logs),
        calls: *x.calls.lock(),
    };
    match ran {
        Ok(_) => Ok(out),
        Err(e) => Err(Error::Adapter {
            instance: b.instance.clone(),
            source: Box::new(e),
        }),
    }
}
