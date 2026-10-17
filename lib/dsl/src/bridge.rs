//! The bridge between a script, which runs synchronously on a blocking thread, and its async host: a `ctx` method
//! that does I/O sends a [`HostRequest`] to the run's task and waits for the answer.

use serde_json::{Map, Value};
use tokio::sync::{mpsc, oneshot};

use crate::{
    Error,
    dsl::{AgentResult, CompleteRequest, CompleteResult, Link, Node},
};

/// An operation that leaves the script.
#[derive(Debug)]
pub(crate) enum HostRequest {
    Complete(CompleteRequest),
    RunAgent {
        name: String,
        intent: String,
    },
    CallTool {
        name: String,
        args: Map<String, Value>,
    },
    Node(String),
    Nodes(String),
    Links {
        key: String,
        direction: String,
        link_type: String,
    },
    /// A connector operation of an adapter.
    Operation {
        operation: String,
        args: Map<String, Value>,
    },
}

/// The answer to a [`HostRequest`].
#[derive(Debug)]
pub(crate) enum HostReply {
    Complete(CompleteResult),
    Agent(AgentResult),
    Value(Value),
    Node(Node),
    Nodes(Vec<Node>),
    Links(Vec<Link>),
    Map(Map<String, Value>),
}

/// A request in flight, with where to answer it.
pub(crate) struct HostCall {
    pub(crate) req: HostRequest,
    pub(crate) reply: oneshot::Sender<Result<HostReply, Error>>,
}

/// The script's end of the bridge.
#[derive(Clone)]
pub(crate) struct Bridge {
    tx: mpsc::UnboundedSender<HostCall>,
}

impl Bridge {
    /// A bridge and the receiver the run serves.
    pub(crate) fn new() -> (Bridge, mpsc::UnboundedReceiver<HostCall>) {
        let (tx, rx) = mpsc::unbounded_channel();
        (Bridge { tx }, rx)
    }

    /// Sends a request and waits for its answer (on the script's thread, never on an async worker).
    pub(crate) fn request(&self, req: HostRequest) -> Result<HostReply, Error> {
        let (tx, rx) = oneshot::channel();
        self.tx
            .send(HostCall { req, reply: tx })
            .map_err(|_| Error::Interrupted("the run is over".into()))?;
        rx.blocking_recv()
            .map_err(|_| Error::Interrupted("the run is over".into()))?
    }
}

/// The reply of an unexpected kind (a bug of the server of the run).
pub(crate) fn unexpected(r: HostReply) -> Error {
    Error::Host(format!("unexpected answer of the host: {r:?}"))
}
