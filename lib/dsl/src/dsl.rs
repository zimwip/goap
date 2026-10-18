//! The action usage: the types of a script job, the [`Host`] it calls and the `ctx` object it sees ([`Ctx`]).

use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_domain::{Blackboard, INTENT_CREATED, INTENT_MODIFIED, TYPE_FLOW};
use parking_lot::Mutex;
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Map, Value};

use crate::{
    Error,
    bridge::{Bridge, HostReply, HostRequest, unexpected},
    decision::{ChangeOption, DecisionPoint},
    engine::ScriptHost,
    gofmt,
    values::{
        arg, arg_bool, arg_f64, arg_map, arg_str, arg_strings, as_list, field_map, field_str,
        map_or_null, num, to_value,
    },
};

pub(crate) fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

fn is_false(b: &bool) -> bool {
    !*b
}

/// Node is a domain node version.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Node {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    pub version: i64,
    #[serde(deserialize_with = "null_default")]
    pub key: String,
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
    /// State in the lifecycle of the node type (empty: none).
    #[serde(deserialize_with = "null_default")]
    pub state: String,
    #[serde(deserialize_with = "null_default")]
    pub props: Map<String, Value>,
}

/// LinkEnd is a link endpoint summary.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct LinkEnd {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    pub version: i64,
    #[serde(deserialize_with = "null_default")]
    pub key: String,
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
}

/// Link is a domain link.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Link {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
    pub from: LinkEnd,
    pub to: LinkEnd,
}

/// Item is a fact of the blackboard snapshot (artifact, decision): the nodes the change acts on are the change
/// impacts.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Item {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
    #[serde(deserialize_with = "null_default")]
    pub target: String,
    #[serde(deserialize_with = "null_default")]
    pub status: String,
    #[serde(deserialize_with = "null_default")]
    pub produced_by: String,
    #[serde(deserialize_with = "null_default")]
    pub data: Map<String, Value>,
}

/// ChangeImpact is a change impact of the blackboard snapshot (ADR 0024): a node the change reads, modifies or
/// creates, with the versions it starts from and produces.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ChangeImpact {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    #[serde(deserialize_with = "null_default")]
    pub key: String,
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
    #[serde(deserialize_with = "null_default")]
    pub intent: String,
    #[serde(deserialize_with = "null_default")]
    pub rationale: String,
    #[serde(deserialize_with = "null_default")]
    pub review: String,
    /// Planned is set while the change impact has no post version yet.
    pub planned: bool,
    /// Drafted is set while the change holds a draft of the node: post is then the draft (Version 0: the node has no
    /// version until the change lands, ADR 0079).
    pub drafted: bool,
    pub pre: Option<Node>,
    pub post: Option<Node>,
    pub landed: Option<Node>,
    /// Links are the outgoing links of the draft (post): from this node to the linked ones.
    #[serde(deserialize_with = "null_default")]
    pub links: Vec<Link>,
    /// Items are the items the change impact is derived from (none when written directly).
    #[serde(deserialize_with = "null_default")]
    pub items: Vec<String>,
}

/// NodeOp is an operation on a change impact buffered by a script; the engine applies them in order after the script
/// ends.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct NodeOp {
    /// Op is declare (ImpactNode: the impact of an existing node; ImpactNodeCreate: held until the node is created by
    /// its first operation), plan (ImpactNodePlan: a creation planned, with no draft, until a write of its key
    /// creates it), write (WriteNode: the draft, the node checked out on the first write, then edited), review
    /// (ImpactNodeReview: an explicit operation, an acceptance runs the checks of a version on the draft), transition
    /// (ImpactNodeTransition), cancel (ImpactNodeCancel), remove (WithdrawImpact) (ADR 0076), merge (ImpactNodeMerge)
    /// or split (ImpactNodeSplit, ADR 0077).
    #[serde(deserialize_with = "null_default")]
    pub op: String,
    /// Ref names a declared change impact ("#nN") for the next operations of the script.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub r#ref: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub intent: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub key: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub r#type: String,
    /// Rationale says why (declare); the comment of a review is in Comment.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub rationale: String,
    /// Node designates the change impact of a write or review: a node key or "#nN".
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub node: String,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub props: Map<String, Value>,
    /// State is the lifecycle state a transition moves the node to.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub state: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub links: Vec<NodeOpLink>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub remove_links: Vec<String>,
    #[serde(skip_serializing_if = "is_false")]
    pub accept: bool,
    /// Reserve is the key of the derogation an acceptance stands on (accepted with reserve, ADR 0075 §2).
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub reserve: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub comment: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub produced_by: String,
    /// Sources are the nodes a merge replaces (keys or "#nN"); a split names its source in Node. Into are the nodes
    /// they are replaced by (ADR 0077).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub sources: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub into: Vec<NodeInto>,
}

/// NodeInto is a node a merge or a split creates; Ref names it for the next operations of the script.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct NodeInto {
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub r#ref: String,
    #[serde(deserialize_with = "null_default")]
    pub key: String,
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub rationale: String,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub props: Map<String, Value>,
}

/// NodeOpLink is an outgoing link added by a write: To is a node key or "#nN".
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct NodeOpLink {
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
    #[serde(deserialize_with = "null_default")]
    pub to: String,
    /// Props are the attributes of the link.
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub props: Map<String, Value>,
}

/// CompleteRequest is an LLM request.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct CompleteRequest {
    #[serde(deserialize_with = "null_default")]
    pub model: String,
    #[serde(deserialize_with = "null_default")]
    pub system: String,
    #[serde(deserialize_with = "null_default")]
    pub prompt: String,
    #[serde(deserialize_with = "null_default")]
    pub json: bool,
    #[serde(deserialize_with = "null_default")]
    pub max_tokens: i64,
}

/// CompleteResult is an LLM answer.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct CompleteResult {
    #[serde(deserialize_with = "null_default")]
    pub text: String,
    /// The JSON of the answer (`null`: none).
    pub json: Value,
    #[serde(deserialize_with = "null_default")]
    pub model: String,
    pub input_tokens: i64,
    pub output_tokens: i64,
}

/// AgentResult is the outcome of a sub-agent run.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct AgentResult {
    #[serde(deserialize_with = "null_default")]
    pub status: String,
    #[serde(deserialize_with = "null_default")]
    pub goal: String,
    #[serde(deserialize_with = "null_default")]
    pub process_id: String,
}

/// LogLine is a script log line.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct LogLine {
    #[serde(with = "goap_domain::gotime")]
    pub time: DateTime<Utc>,
    pub level: String,
    pub message: String,
}

impl LogLine {
    pub(crate) fn now(level: &str, message: String) -> Self {
        LogLine {
            time: Utc::now(),
            level: level.to_string(),
            message,
        }
    }
}

/// Host performs the operations that leave the script: in a sandbox it is a client of the engine RuntimeService.
#[async_trait]
pub trait Host: Send + Sync {
    async fn complete(
        &self,
        ctx: &goap_ctx::Ctx,
        req: CompleteRequest,
    ) -> Result<CompleteResult, Error>;
    /// Runs a sub-agent on the same change; [`Error::Suspended`] when it waits for a human.
    async fn run_agent(
        &self,
        ctx: &goap_ctx::Ctx,
        name: &str,
        intent: &str,
    ) -> Result<AgentResult, Error>;
    async fn call_tool(
        &self,
        ctx: &goap_ctx::Ctx,
        name: &str,
        args: Map<String, Value>,
    ) -> Result<Value, Error>;
    async fn node(&self, ctx: &goap_ctx::Ctx, key: &str) -> Result<Node, Error>;
    async fn nodes(&self, ctx: &goap_ctx::Ctx, node_type: &str) -> Result<Vec<Node>, Error>;
    async fn links(
        &self,
        ctx: &goap_ctx::Ctx,
        key: &str,
        direction: &str,
        link_type: &str,
    ) -> Result<Vec<Link>, Error>;
}

/// The duration of a job in nanoseconds, as `encoding/json` writes a `time.Duration`.
mod nanos {
    use std::time::Duration;

    use serde::{Deserialize, Deserializer, Serializer};

    pub fn serialize<S: Serializer>(d: &Duration, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_i64(i64::try_from(d.as_nanos()).unwrap_or(i64::MAX))
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<Duration, D::Error> {
        let n = Option::<i64>::deserialize(d)?.unwrap_or_default();
        Ok(Duration::from_nanos(u64::try_from(n).unwrap_or(0)))
    }
}

/// Job is a script execution.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Job {
    #[serde(deserialize_with = "null_default")]
    pub language: String,
    #[serde(deserialize_with = "null_default")]
    pub code: String,
    #[serde(deserialize_with = "null_default")]
    pub process_id: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub parent_id: String,
    #[serde(deserialize_with = "null_default")]
    pub agent: String,
    #[serde(deserialize_with = "null_default")]
    pub action: String,
    #[serde(deserialize_with = "null_default")]
    pub intent: String,
    #[serde(deserialize_with = "null_default")]
    pub goal: String,
    #[serde(deserialize_with = "null_default")]
    pub params: Map<String, Value>,
    #[serde(deserialize_with = "null_default")]
    pub vars: Map<String, Value>,
    #[serde(deserialize_with = "null_default")]
    pub items: Vec<Item>,
    #[serde(deserialize_with = "null_default")]
    pub nodes: Vec<ChangeImpact>,
    /// Options and DecisionPoints of the change (ADR 0009 §3-4).
    #[serde(deserialize_with = "null_default")]
    pub options: Vec<ChangeOption>,
    #[serde(deserialize_with = "null_default")]
    pub decision_points: Vec<DecisionPoint>,
    /// The time limit of the run (zero: [`crate::DEFAULT_TIMEOUT`]); nanoseconds in JSON.
    #[serde(with = "nanos")]
    pub timeout: Duration,
}

/// Result is the outcome of a script (Go's `dsl.Result`): the items to add to the change (engine ItemInput format),
/// logs and whether the script was suspended.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ScriptResult {
    #[serde(deserialize_with = "null_default")]
    pub items: Vec<Map<String, Value>>,
    /// Nodes are the change impact operations, in order (the engine applies them).
    #[serde(deserialize_with = "null_default")]
    pub nodes: Vec<NodeOp>,
    #[serde(deserialize_with = "null_default")]
    pub output: String,
    #[serde(deserialize_with = "null_default")]
    pub logs: Vec<LogLine>,
    pub suspended: bool,
    /// WakeOn lists signal names that, if emitted (addressed to this process) before a suspended sub-agent
    /// terminates, wake this process early.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub wake_on: Vec<String>,
    /// VarsSet holds the process-private variables set by SetVar during this run (engine-owned execution state, not
    /// a graph write).
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub vars_set: Map<String, Value>,
}

#[derive(Default)]
pub(crate) struct CtxState {
    pub(crate) out: Vec<Map<String, Value>>,
    pub(crate) node_ops: Vec<NodeOp>,
    nseq: usize,
    dseq: usize,
    pub(crate) logs: Vec<LogLine>,
    seq: usize,
    pub(crate) suspended: bool,
    pub(crate) wake_on: Vec<String>,
    pub(crate) vars: Map<String, Value>,
    pub(crate) result: Option<Value>,
}

impl CtxState {
    fn next_node_ref(&mut self) -> String {
        self.nseq += 1;
        format!("#n{}", self.nseq)
    }
}

/// Ctx is the object injected as `ctx` in scripts. Its methods are exposed in camelCase to JavaScript
/// (`impactNode`, `changeImpacts`, `llm`, `processID`...). Writes are buffered; the run returns them once the script
/// ends without error.
pub struct Ctx {
    job: Job,
    bridge: Option<Bridge>,
    pub(crate) state: Mutex<CtxState>,
}

/// The names of the methods of [`Ctx`] a script sees (Go's method names, first letter lowered; `LLM` → `llm`).
const CTX_METHODS: &[&str] = &[
    "intent",
    "goal",
    "agent",
    "action",
    "processID",
    "parentID",
    "param",
    "var",
    "setVar",
    "wakeOn",
    "items",
    "changeImpacts",
    "options",
    "decisionPoints",
    "node",
    "nodes",
    "links",
    "impactNode",
    "impactNodeCreate",
    "impactNodePlan",
    "impactNodeMerge",
    "impactNodeSplit",
    "writeNode",
    "impactNodeReview",
    "impactNodeReviewWithReserve",
    "impactNodeTransition",
    "impactNodeCancel",
    "withdrawImpact",
    "addArtifact",
    "signal",
    "openDecision",
    "decide",
    "undecidable",
    "answer",
    "llm",
    "complete",
    "runAgent",
    "callTool",
    "log",
    "warn",
    "setOutput",
];

impl Ctx {
    /// The context of a job with no host: the calls that leave the script fail.
    pub fn new(job: Job) -> Self {
        Ctx {
            job,
            bridge: None,
            state: Mutex::default(),
        }
    }

    pub(crate) fn with_bridge(job: Job, bridge: Bridge) -> Self {
        Ctx {
            job,
            bridge: Some(bridge),
            state: Mutex::default(),
        }
    }

    fn request(&self, req: HostRequest) -> Result<HostReply, Error> {
        match &self.bridge {
            Some(b) => b.request(req),
            None => Err(Error::host("no host")),
        }
    }

    // ---- context ----------------------------------------------------------------

    pub fn intent(&self) -> &str {
        &self.job.intent
    }
    pub fn goal(&self) -> &str {
        &self.job.goal
    }
    pub fn agent(&self) -> &str {
        &self.job.agent
    }
    pub fn action(&self) -> &str {
        &self.job.action
    }
    pub fn process_id(&self) -> &str {
        &self.job.process_id
    }

    /// ParentID is the process that called this one as a sub-agent (empty if none): the target to address a Signal
    /// to so the parent may wake on it.
    pub fn parent_id(&self) -> &str {
        &self.job.parent_id
    }

    /// Param returns an action parameter (`null` when absent).
    pub fn param(&self, name: &str) -> Value {
        self.job.params.get(name).cloned().unwrap_or(Value::Null)
    }

    /// Var returns a process variable (`null` when absent).
    pub fn var(&self, name: &str) -> Value {
        self.job.vars.get(name).cloned().unwrap_or(Value::Null)
    }

    /// SetVar sets a process-private variable: engine-owned execution state, not a graph write, visible only to this
    /// process's own conditions/scripts.
    pub fn set_var(&self, name: &str, v: Value) {
        self.state.lock().vars.insert(name.to_string(), v);
    }

    /// WakeOn declares signal names that, if emitted (addressed to this process) before a sub-agent this script
    /// suspends on terminates, wake it early.
    pub fn wake_on(&self, names: impl IntoIterator<Item = String>) {
        self.state.lock().wake_on.extend(names);
    }

    // ---- blackboard (read) ----------------------------------------------------

    /// Items returns the change items of a type ("" = all).
    pub fn items(&self, typ: &str) -> Vec<Item> {
        self.job
            .items
            .iter()
            .filter(|it| typ.is_empty() || it.r#type == typ)
            .cloned()
            .collect()
    }

    /// ChangeImpacts returns the change impacts of the change: the stored ones and the ones derived from its items.
    pub fn change_impacts(&self) -> &[ChangeImpact] {
        &self.job.nodes
    }

    /// Options returns the options of the change.
    pub fn options(&self) -> &[ChangeOption] {
        &self.job.options
    }

    /// DecisionPoints returns the decision points of the change.
    pub fn decision_points(&self) -> &[DecisionPoint] {
        &self.job.decision_points
    }

    // ---- domain (read, reference baseline) --------------------------------------

    pub(crate) fn node(&self, key: &str) -> Result<Node, Error> {
        match self.request(HostRequest::Node(key.to_string()))? {
            HostReply::Node(n) => Ok(n),
            r => Err(unexpected(r)),
        }
    }

    pub(crate) fn nodes(&self, node_type: &str) -> Result<Vec<Node>, Error> {
        match self.request(HostRequest::Nodes(node_type.to_string()))? {
            HostReply::Nodes(n) => Ok(n),
            r => Err(unexpected(r)),
        }
    }

    pub(crate) fn links(
        &self,
        key: &str,
        direction: &str,
        link_type: &str,
    ) -> Result<Vec<Link>, Error> {
        let req = HostRequest::Links {
            key: key.to_string(),
            direction: direction.to_string(),
            link_type: link_type.to_string(),
        };
        match self.request(req)? {
            HostReply::Links(l) => Ok(l),
            r => Err(unexpected(r)),
        }
    }

    // ---- change (buffered writes) ---------------------------------------------

    fn emit(&self, mut item: Map<String, Value>) -> String {
        let mut st = self.state.lock();
        st.seq += 1;
        let r = format!("p{}", st.seq);
        item.insert("ref".into(), Value::from(r.clone()));
        item.insert("producedBy".into(), Value::from(self.job.action.clone()));
        st.out.push(item);
        format!("#{r}")
    }

    fn push(&self, op: NodeOp) {
        self.state.lock().node_ops.push(op);
    }

    fn op(&self, op: &str) -> NodeOp {
        NodeOp {
            op: op.to_string(),
            produced_by: self.job.action.clone(),
            ..Default::default()
        }
    }

    /// ImpactNode declares that the change acts on an existing node of the reference baseline, and why: a change
    /// impact with no version written yet. It returns a reference ("#nN") for WriteNode and ImpactNodeReview of the
    /// same script.
    pub fn impact_node(&self, key: &str, rationale: &str) -> String {
        let mut st = self.state.lock();
        let r = st.next_node_ref();
        st.node_ops.push(NodeOp {
            r#ref: r.clone(),
            intent: INTENT_MODIFIED.to_string(),
            key: key.to_string(),
            rationale: rationale.to_string(),
            ..self.op("declare")
        });
        r
    }

    /// ImpactNodeCreate declares that the change creates a node of a type, and why. A new node has no proposal (ADR
    /// 0077): the engine creates it (one created event: its impact and first version) at the first write that names
    /// the reference, with the properties it carries, else bare when the script ends.
    pub fn impact_node_create(&self, node_type: &str, key: &str, rationale: &str) -> String {
        let mut st = self.state.lock();
        let r = st.next_node_ref();
        st.node_ops.push(NodeOp {
            r#ref: r.clone(),
            intent: INTENT_CREATED.to_string(),
            key: key.to_string(),
            r#type: node_type.to_string(),
            rationale: rationale.to_string(),
            ..self.op("declare")
        });
        r
    }

    /// ImpactNodePlan plans the creation of a node of a type, and why, without writing it: a change impact of intent
    /// created with no draft (what an impact analysis finds the change will create). A later WriteNode of its key (in
    /// this script or another action) creates it; a change cannot merge while it holds a planned creation (create or
    /// withdraw it). It returns a reference ("#nN") for the next operations of the script.
    pub fn impact_node_plan(&self, node_type: &str, key: &str, rationale: &str) -> String {
        let mut st = self.state.lock();
        let r = st.next_node_ref();
        st.node_ops.push(NodeOp {
            r#ref: r.clone(),
            intent: INTENT_CREATED.to_string(),
            key: key.to_string(),
            r#type: node_type.to_string(),
            rationale: rationale.to_string(),
            ..self.op("plan")
        });
        r
    }

    /// ImpactNodeMerge merges nodes (keys, or references of this script) into a new node of a type, seen from their
    /// parents (ADR 0077): the parents lose the links to the sources and gain one to the new node, the sources stay as
    /// they are. It returns the reference of the new node, with its draft. Every source needs a parent (a link of a
    /// type flagged compose to it).
    pub fn impact_node_merge(
        &self,
        sources: Vec<String>,
        node_type: &str,
        key: &str,
        rationale: &str,
    ) -> String {
        let mut st = self.state.lock();
        let r = st.next_node_ref();
        st.node_ops.push(NodeOp {
            sources,
            into: vec![NodeInto {
                r#ref: r.clone(),
                key: key.to_string(),
                r#type: node_type.to_string(),
                rationale: rationale.to_string(),
                props: Map::new(),
            }],
            rationale: rationale.to_string(),
            ..self.op("merge")
        });
        r
    }

    /// ImpactNodeSplit splits a node (key, or reference) into new nodes, [{type, key, rationale, props}], seen from
    /// its parents (ADR 0077). It returns their references in order.
    pub fn impact_node_split(&self, source: &str, into: &[Map<String, Value>]) -> Vec<String> {
        let mut st = self.state.lock();
        let mut op = NodeOp {
            node: source.to_string(),
            ..self.op("split")
        };
        let mut refs = Vec::new();
        for m in into {
            let r = st.next_node_ref();
            refs.push(r.clone());
            op.into.push(NodeInto {
                r#ref: r,
                key: field_str(m, "key"),
                r#type: field_str(m, "type"),
                rationale: field_str(m, "rationale"),
                props: field_map(m, "props").unwrap_or_default(),
            });
        }
        if let Some(first) = op.into.first() {
            op.rationale = first.rationale.clone();
        }
        st.node_ops.push(op);
        refs
    }

    /// WriteNode edits the draft of a declared node (ADR 0076, 0079): the first write checks the node out (a draft: no
    /// version is written until the change lands), the next ones edit it. A write never reviews. node is a key of a
    /// change impact or the reference returned by ImpactNode / ImpactNodeCreate; w may hold props (merged over the
    /// current ones), links ([{type, to}] with to a node key or a reference of a node written earlier) and
    /// removeLinks (link ids). A lifecycle state is a transition (ImpactNodeTransition); a node is removed by its
    /// parent losing the link to it.
    pub fn write_node(&self, node: &str, w: &Map<String, Value>) {
        let mut op = NodeOp {
            node: node.to_string(),
            ..self.op("write")
        };
        op.props = field_map(w, "props").unwrap_or_default();
        for x in as_list(w.get("links")) {
            if let Value::Object(m) = x {
                op.links.push(NodeOpLink {
                    r#type: field_str(m, "type"),
                    to: field_str(m, "to"),
                    props: Map::new(),
                });
            }
        }
        for x in as_list(w.get("removeLinks")) {
            if let Value::String(id) = x {
                op.remove_links.push(id.clone());
            }
        }
        self.push(op);
    }

    /// ImpactNodeReview accepts or rejects a change impact; the comment is mandatory.
    pub fn impact_node_review(&self, node: &str, accept: bool, comment: &str) {
        self.push(NodeOp {
            node: node.to_string(),
            accept,
            comment: comment.to_string(),
            ..self.op("review")
        });
    }

    /// ImpactNodeReviewWithReserve accepts a change impact with a reserve: the derogation (its key) must be open,
    /// unexpired and target the impact or its action, else the engine refuses the review (ADR 0075).
    pub fn impact_node_review_with_reserve(&self, node: &str, derogation: &str, comment: &str) {
        self.push(NodeOp {
            node: node.to_string(),
            accept: true,
            reserve: derogation.to_string(),
            comment: comment.to_string(),
            ..self.op("review")
        });
    }

    /// ImpactNodeTransition moves the node of a change impact to a lifecycle state: the state of its draft, which a
    /// node with none is checked out for first; authorized and guarded when it is taken, the guard seeing the
    /// change, the impact and the draft (ADR 0076, 0079).
    pub fn impact_node_transition(&self, node: &str, state: &str) {
        self.push(NodeOp {
            node: node.to_string(),
            state: state.to_string(),
            ..self.op("transition")
        });
    }

    /// ImpactNodeCancel drops the draft of a change impact; a creation cancelled removes its impact, no node was
    /// written (ADR 0076, 0079).
    pub fn impact_node_cancel(&self, node: &str) {
        self.push(NodeOp {
            node: node.to_string(),
            ..self.op("cancel")
        });
    }

    /// WithdrawImpact takes a change impact out of the change: its draft is dropped (a creation leaves no node) (ADR
    /// 0076, 0079).
    pub fn withdraw_impact(&self, node: &str) {
        self.push(NodeOp {
            node: node.to_string(),
            ..self.op("remove")
        });
    }

    /// AddArtifact records free data (report…); `data` `None` is Go's nil map (`null`).
    pub fn add_artifact(&self, artifact_type: &str, data: Option<Map<String, Value>>) -> String {
        let mut m = Map::new();
        m.insert("type".into(), "artifact".into());
        m.insert("target".into(), artifact_type.into());
        m.insert("data".into(), map_or_null(data));
        self.emit(m)
    }

    /// Signal emits a named notification other agents or a live parent may react to. recipient addresses a process id
    /// ("" = broadcast, visible to triggers and to readers of the change).
    pub fn signal(&self, name: &str, data: Option<Map<String, Value>>, recipient: &str) -> String {
        let mut m = Map::new();
        m.insert("type".into(), "signal".into());
        m.insert("target".into(), name.into());
        m.insert("data".into(), map_or_null(data));
        m.insert("recipient".into(), recipient.into());
        self.emit(m)
    }

    /// OpenDecision opens a decision point on a question; spec may hold options (names or ids; none: the open
    /// options), criteria; any other key is a policy value of the point (decider, threshold, maxRounds, maxDuration
    /// ("48h"): the keys of the decision policy, which the script does not interpret). It returns a reference ("#dN")
    /// for the rulings of the same script.
    pub fn open_decision(&self, question: &str, spec: Option<&Map<String, Value>>) -> String {
        let r = {
            let mut st = self.state.lock();
            st.dseq += 1;
            format!("#d{}", st.dseq)
        };
        let mut op = Map::new();
        op.insert("op".into(), goap_domain::DECISION_OPEN_OP.into());
        op.insert("ref".into(), r.clone().into());
        op.insert("question".into(), question.into());
        let mut policy = Map::new();
        for (k, v) in spec.into_iter().flatten() {
            match k.as_str() {
                "options" | "criteria" => {
                    op.insert(k.clone(), v.clone());
                }
                _ => {
                    policy.insert(k.clone(), v.clone());
                }
            }
        }
        if !policy.is_empty() {
            op.insert("policy".into(), Value::Object(policy));
        }
        self.emit_decision(op);
        r
    }

    fn emit_decision(&self, op: Map<String, Value>) {
        let mut m = Map::new();
        m.insert("type".into(), "decisionPoint".into());
        m.insert("decisionPoint".into(), Value::Object(op));
        self.emit(m);
    }

    /// Decide rules a decision point decided: the option (name or id), the confidence (0 to 1) and why. point is an
    /// id, a "#dN" reference, or "" for the only pending point.
    pub fn decide(&self, point: &str, option: &str, confidence: f64, justification: &str) {
        let mut op = Map::new();
        op.insert("op".into(), goap_domain::DECISION_RULE_OP.into());
        op.insert("point".into(), point.into());
        op.insert("outcome".into(), goap_domain::OUTCOME_DECIDED.into());
        op.insert("option".into(), option.into());
        op.insert("confidence".into(), num(confidence));
        op.insert("justification".into(), justification.into());
        self.emit_decision(op);
    }

    /// Undecidable rules a decision point undecidable: why, and the questions that must be answered first.
    pub fn undecidable(&self, point: &str, justification: &str, questions: Vec<String>) {
        let mut op = Map::new();
        op.insert("op".into(), goap_domain::DECISION_RULE_OP.into());
        op.insert("point".into(), point.into());
        op.insert("outcome".into(), goap_domain::OUTCOME_UNDECIDABLE.into());
        op.insert("justification".into(), justification.into());
        op.insert("questions".into(), questions.into());
        self.emit_decision(op);
    }

    /// Answer answers an open question of a decision point.
    pub fn answer(&self, question_id: &str, answer: &str) {
        let mut op = Map::new();
        op.insert("op".into(), goap_domain::DECISION_ANSWER_OP.into());
        op.insert("questionId".into(), question_id.into());
        op.insert("answer".into(), answer.into());
        self.emit_decision(op);
    }

    // ---- platform calls ---------------------------------------------------------

    /// LLM completes a prompt with the default model and returns the text.
    pub(crate) fn llm(&self, prompt: &str) -> Result<String, Error> {
        self.complete(CompleteRequest {
            prompt: prompt.to_string(),
            ..Default::default()
        })
        .map(|r| r.text)
    }

    /// Complete calls the model gateway.
    pub(crate) fn complete(&self, mut req: CompleteRequest) -> Result<CompleteResult, Error> {
        if req.model.is_empty() {
            req.model = "default".into();
        }
        let json = req.json;
        let mut r = match self.request(HostRequest::Complete(req))? {
            HostReply::Complete(r) => r,
            r => return Err(unexpected(r)),
        };
        if json
            && r.json.is_null()
            && let Ok(v) = serde_json::from_str::<Value>(extract_json(&r.text))
        {
            r.json = v;
        }
        Ok(r)
    }

    /// RunAgent runs a sub-agent on the same change; a sub-agent waiting for a human suspends the script.
    pub(crate) fn run_agent(&self, name: &str, intent: &str) -> Result<AgentResult, Error> {
        let req = HostRequest::RunAgent {
            name: name.to_string(),
            intent: intent.to_string(),
        };
        match self.request(req) {
            Ok(HostReply::Agent(r)) => Ok(r),
            Ok(r) => Err(unexpected(r)),
            Err(e) => {
                if e.is_suspended() {
                    self.state.lock().suspended = true;
                }
                Err(e)
            }
        }
    }

    /// CallTool calls an MCP tool ("server/tool").
    pub(crate) fn call_tool(&self, name: &str, args: Map<String, Value>) -> Result<Value, Error> {
        let req = HostRequest::CallTool {
            name: name.to_string(),
            args,
        };
        match self.request(req)? {
            HostReply::Value(v) => Ok(v),
            r => Err(unexpected(r)),
        }
    }

    // ---- logs -------------------------------------------------------------------

    pub(crate) fn logf(&self, level: &str, msg: String) {
        self.state.lock().logs.push(LogLine::now(level, msg));
    }

    pub fn log(&self, msg: &str) {
        self.logf("info", msg.to_string());
    }

    pub fn warn(&self, msg: &str) {
        self.logf("warn", msg.to_string());
    }

    /// SetOutput sets the human readable output of the action.
    pub fn set_output(&self, v: Value) {
        self.state.lock().result = Some(v);
    }
}

impl ScriptHost for Ctx {
    fn methods(&self) -> &'static [&'static str] {
        CTX_METHODS
    }

    fn call(&self, method: &str, a: &[Value]) -> Result<Option<Value>, String> {
        let s = |i| arg_str(a, i);
        let done = |v: Value| Ok(Some(v));
        let fail = |e: Error| Err(e.to_string());
        match method {
            "intent" => done(self.intent().into()),
            "goal" => done(self.goal().into()),
            "agent" => done(self.agent().into()),
            "action" => done(self.action().into()),
            "processID" => done(self.process_id().into()),
            "parentID" => done(self.parent_id().into()),
            "param" => done(self.param(&s(0))),
            "var" => done(self.var(&s(0))),
            "setVar" => {
                self.set_var(&s(0), arg(a, 1).clone());
                Ok(None)
            }
            "wakeOn" => {
                self.wake_on((0..a.len()).map(s));
                Ok(None)
            }
            "items" => done(to_value(&self.items(&s(0)))),
            "changeImpacts" => done(to_value(&self.change_impacts())),
            "options" => done(to_value(&self.options())),
            "decisionPoints" => done(to_value(&self.decision_points())),
            "node" => self.node(&s(0)).map(|n| Some(to_value(&n))).or_else(fail),
            "nodes" => self.nodes(&s(0)).map(|n| Some(to_value(&n))).or_else(fail),
            "links" => self
                .links(&s(0), &s(1), &s(2))
                .map(|l| Some(to_value(&l)))
                .or_else(fail),
            "impactNode" => done(self.impact_node(&s(0), &s(1)).into()),
            "impactNodeCreate" => done(self.impact_node_create(&s(0), &s(1), &s(2)).into()),
            "impactNodePlan" => done(self.impact_node_plan(&s(0), &s(1), &s(2)).into()),
            "impactNodeMerge" => done(
                self.impact_node_merge(arg_strings(a, 0), &s(1), &s(2), &s(3))
                    .into(),
            ),
            "impactNodeSplit" => {
                let into: Vec<Map<String, Value>> = match arg(a, 1) {
                    Value::Array(l) => l
                        .iter()
                        .map(|x| x.as_object().cloned().unwrap_or_default())
                        .collect(),
                    _ => Vec::new(),
                };
                done(self.impact_node_split(&s(0), &into).into())
            }
            "writeNode" => {
                self.write_node(&s(0), &arg_map(a, 1).unwrap_or_default());
                Ok(None)
            }
            "impactNodeReview" => {
                self.impact_node_review(&s(0), arg_bool(a, 1), &s(2));
                Ok(None)
            }
            "impactNodeReviewWithReserve" => {
                self.impact_node_review_with_reserve(&s(0), &s(1), &s(2));
                Ok(None)
            }
            "impactNodeTransition" => {
                self.impact_node_transition(&s(0), &s(1));
                Ok(None)
            }
            "impactNodeCancel" => {
                self.impact_node_cancel(&s(0));
                Ok(None)
            }
            "withdrawImpact" => {
                self.withdraw_impact(&s(0));
                Ok(None)
            }
            "addArtifact" => done(self.add_artifact(&s(0), arg_map(a, 1)).into()),
            "signal" => done(self.signal(&s(0), arg_map(a, 1), &s(2)).into()),
            "openDecision" => done(self.open_decision(&s(0), arg_map(a, 1).as_ref()).into()),
            "decide" => {
                self.decide(&s(0), &s(1), arg_f64(a, 2), &s(3));
                Ok(None)
            }
            "undecidable" => {
                self.undecidable(&s(0), &s(1), arg_strings(a, 2));
                Ok(None)
            }
            "answer" => {
                self.answer(&s(0), &s(1));
                Ok(None)
            }
            "llm" => self.llm(&s(0)).map(|t| Some(t.into())).or_else(fail),
            "complete" => {
                let req: CompleteRequest =
                    serde_json::from_value(arg(a, 0).clone()).map_err(|e| e.to_string())?;
                self.complete(req).map(|r| Some(to_value(&r))).or_else(fail)
            }
            "runAgent" => self
                .run_agent(&s(0), &s(1))
                .map(|r| Some(to_value(&r)))
                .or_else(fail),
            "callTool" => self
                .call_tool(&s(0), arg_map(a, 1).unwrap_or_default())
                .map(Some)
                .or_else(fail),
            "log" => {
                self.log(&s(0));
                Ok(None)
            }
            "warn" => {
                self.warn(&s(0));
                Ok(None)
            }
            "setOutput" => {
                self.set_output(arg(a, 0).clone());
                Ok(None)
            }
            _ => Err(format!("ctx has no method {method}")),
        }
    }

    fn log(&self, level: &str, msg: String) {
        self.logf(level, msg);
    }
}

/// The JSON of an answer: from its first `{` or `[`.
fn extract_json(s: &str) -> &str {
    match s.find(['{', '[']) {
        Some(i) => &s[i..],
        None => s,
    }
}

/// The output of a script: Go's `fmt.Sprint` of the value it returned or set.
pub(crate) fn output_of(v: &Value) -> String {
    gofmt::value(v)
}

/// ItemsFromBlackboard builds the snapshot of the facts (artifacts, decisions) given to scripts.
pub fn items_from_blackboard(bb: &Blackboard) -> Vec<Item> {
    bb.change
        .items
        .iter()
        .filter(|it| bb.change.active(&it.id) && it.r#type != TYPE_FLOW)
        .map(|it| Item {
            id: it.id.to_string(),
            r#type: it.r#type.to_string(),
            target: it.target.clone(),
            status: bb.change.effective_status(&it.id).to_string(),
            produced_by: it.produced_by.clone(),
            data: it.data.clone(),
        })
        .collect()
}
