//! The cost analysis of a run (`analyze.go`).

use std::collections::{BTreeMap, HashMap, HashSet};

use goap_domain::{ChangeImpact, ChangeImpactID, ChangeItem, ItemID};
use goap_journal::{KIND_ACTION, KIND_PROCESS_ENDED, KIND_PROCESS_STARTED, KIND_TICK, Record};
use serde::{Deserialize, Serialize};

use crate::util::{
    go_float, is_false, is_zero_f64, is_zero_i64, null_default, null_if_empty, quote,
};

// Finding kinds.
/// An action executed again and again.
pub const FIND_LOOP: &str = "loop";
/// Executions without the promised effects.
pub const FIND_FAILURE: &str = "failure";
/// Actions disabled after repeated failures.
pub const FIND_DISABLED: &str = "disabled";
/// An action concentrating model tokens / calls.
pub const FIND_LLM_HEAVY: &str = "llm_heavy";
/// Model calls producing regular, systematizable output.
pub const FIND_SYSTEMATIZABLE: &str = "llm_systematizable";
/// The plan changed course often.
pub const FIND_REPLANNING: &str = "replanning";
/// Long executions.
pub const FIND_SLOW_ACTION: &str = "slow_action";
/// Slow operations seen in the traces (tools, model calls).
pub const FIND_SLOW_SPAN: &str = "slow_span";

/// Thresholds tune the analysis (a zero field takes its default).
#[derive(Debug, Clone, Copy, Default, PartialEq)]
pub struct Thresholds {
    /// Executions of one action in a run (default 3).
    pub loop_executions: i64,
    /// Executions without effects (default 2).
    pub failures: i64,
    /// Share of the run tokens (default 0.4).
    pub llm_token_share: f64,
    /// Model calls of one action (default 3).
    pub llm_calls: i64,
    /// Plan changes in a run (default 3).
    pub replans: i64,
    /// Default 30 s.
    pub slow_action_ms: i64,
    /// Default 10 s.
    pub slow_span_ms: i64,
    /// Items needed to judge an output regular (default 2).
    pub min_systematized: i64,
}

impl Thresholds {
    fn with_defaults(mut self) -> Self {
        fn def(v: &mut i64, d: i64) {
            if *v == 0 {
                *v = d;
            }
        }
        def(&mut self.loop_executions, 3);
        def(&mut self.failures, 2);
        def(&mut self.llm_calls, 3);
        def(&mut self.replans, 3);
        def(&mut self.min_systematized, 2);
        if self.llm_token_share == 0.0 {
            self.llm_token_share = 0.4;
        }
        def(&mut self.slow_action_ms, 30_000);
        def(&mut self.slow_span_ms, 10_000);
        self
    }
}

/// Span is an OpenTelemetry span of the observed run (from the trace backend).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Span {
    pub name: String,
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "is_false")]
    pub error: bool,
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub attributes: BTreeMap<String, String>,
}

/// ActionStats aggregates the executions of one action.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ActionStats {
    pub action: String,
    pub kind: String,
    pub agent: String,
    pub executions: i64,
    pub failures: i64,
    pub model_calls: i64,
    pub tool_calls: i64,
    pub tokens: i64,
    pub duration_ms: i64,
    pub max_duration_ms: i64,
    /// Produced items by kind[/type|op].
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub outputs: BTreeMap<String, i64>,
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub specializations: BTreeMap<String, i64>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub records: Vec<String>,
}

/// SpanStats aggregates spans by name.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct SpanStats {
    pub name: String,
    pub count: i64,
    pub errors: i64,
    pub duration_ms: i64,
    pub max_duration_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub action: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub tool: String,
}

/// Finding is a costly element of the methodology.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Finding {
    pub kind: String,
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub action: String,
    /// Element is the key of the definition node of the element the finding is about.
    pub element: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub count: i64,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub tokens: i64,
    #[serde(rename = "durationMs", skip_serializing_if = "is_zero_i64")]
    pub duration: i64,
    #[serde(skip_serializing_if = "is_zero_f64", serialize_with = "go_float")]
    pub share: f64,
    pub evidence: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub records: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub span: String,
}

/// Report is the cost analysis of a run (a process and its sub-agents).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Report {
    pub process: String,
    pub change: String,
    pub methodology: String,
    pub version: String,
    pub agent: String,
    pub status: String,
    pub processes: i64,
    pub ticks: i64,
    pub replans: i64,
    pub steps: i64,
    pub tokens: i64,
    pub model_calls: i64,
    pub tool_calls: i64,
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub trace_id: String,
    #[serde(serialize_with = "null_if_empty", deserialize_with = "null_default")]
    pub actions: Vec<ActionStats>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub spans: Vec<SpanStats>,
    #[serde(serialize_with = "null_if_empty", deserialize_with = "null_default")]
    pub findings: Vec<Finding>,
}

/// ElementKey returns the key of the definition node of an element of a methodology version (ADR 0023):
/// "MV:<name>@<version>" for the version itself, "MV:<name>@<version>/<kind>/<element>" for an element.
pub fn element_key(meth: &str, version: &str, kind: &str, name: &str) -> String {
    if kind == "methodology" {
        return format!("MV:{meth}@{version}");
    }
    format!("MV:{meth}@{version}/{kind}/{name}")
}

// Types of the definition nodes the self-observation proposes (meta-domain methodology, ADR 0012 §4).
pub const TYPE_ACTION: &str = "methodology@Action";
pub const TYPE_TOOL_REQUEST: &str = "methodology@ToolRequest";

fn output_key(it: &ChangeItem) -> String {
    if !it.target.is_empty() {
        return format!("{}/{}", it.r#type, it.target);
    }
    it.r#type.to_string()
}

/// Analyze computes the cost report of a run from its journal records, the items and change impacts of the observed
/// change and the spans of its trace (optional).
pub fn analyze(
    recs: &[Record],
    items: &HashMap<ItemID, ChangeItem>,
    nodes: &HashMap<ChangeImpactID, ChangeImpact>,
    spans: &[Span],
    th: Thresholds,
) -> Report {
    let th = th.with_defaults();
    let mut r = Report::default();
    let mut stats: HashMap<String, ActionStats> = HashMap::new();
    let mut procs: HashSet<&str> = HashSet::new();
    let mut order: Vec<String> = Vec::new();
    for rec in recs {
        procs.insert(&rec.process_id);
        if r.process.is_empty()
            || rec.parent_process_id.is_empty() && rec.kind == KIND_PROCESS_STARTED
        {
            r.process = rec.process_id.clone();
            r.change = rec.change_id.to_string();
            r.methodology = rec.methodology.clone();
            r.version = rec.methodology_version.clone();
            r.agent = rec.agent.clone();
        }
        if r.trace_id.is_empty() && rec.parent_process_id.is_empty() {
            r.trace_id = rec.trace_id.clone();
        }
        match rec.kind.as_str() {
            KIND_TICK => {
                r.ticks += 1;
                if rec.data.get("replanned").and_then(|v| v.as_bool()) == Some(true) {
                    r.replans += 1;
                }
            }
            KIND_PROCESS_ENDED => {
                if rec.parent_process_id.is_empty() {
                    r.status = rec.status.clone();
                    r.duration_ms = rec.duration_ms;
                }
            }
            KIND_ACTION => {
                let key = format!("{}/{}", rec.agent, rec.action);
                let s = stats.entry(key.clone()).or_insert_with(|| {
                    order.push(key);
                    ActionStats {
                        action: rec.action.clone(),
                        kind: rec.action_kind.clone(),
                        agent: rec.agent.clone(),
                        ..Default::default()
                    }
                });
                if rec
                    .data
                    .get("waiting")
                    .and_then(|v| v.as_str())
                    .is_some_and(|w| !w.is_empty())
                {
                    continue; // the execution goes on after a human / sub-agent
                }
                r.steps += 1;
                s.executions += 1;
                let progress = rec.data.get("progress").and_then(|v| v.as_bool()) == Some(true);
                if !rec.error.is_empty() || (rec.effects_met == Some(false) && !progress) {
                    s.failures += 1;
                }
                let tokens = rec.input_tokens + rec.output_tokens;
                s.model_calls += rec.model_calls.len() as i64;
                s.tool_calls += rec.tool_calls.len() as i64;
                s.tokens += tokens;
                s.duration_ms += rec.duration_ms;
                s.max_duration_ms = s.max_duration_ms.max(rec.duration_ms);
                if !rec.specialization.is_empty() {
                    *s.specializations
                        .entry(rec.specialization.clone())
                        .or_default() += 1;
                }
                for id in &rec.items {
                    if let Some(it) = items.get(id) {
                        *s.outputs.entry(output_key(it)).or_default() += 1;
                    }
                }
                for id in &rec.nodes {
                    if let Some(n) = nodes.get(id) {
                        *s.outputs
                            .entry(format!("changeImpact/{}", n.intent))
                            .or_default() += 1;
                    }
                }
                s.records.push(rec.id.clone());
                r.tokens += tokens;
                r.model_calls += rec.model_calls.len() as i64;
                r.tool_calls += rec.tool_calls.len() as i64;
            }
            _ => {}
        }
    }
    r.processes = procs.len() as i64;
    for k in &order {
        if let Some(s) = stats.remove(k) {
            r.actions.push(s);
        }
    }
    r.spans = aggregate_spans(spans);
    r.findings = findings(&r, &th);
    r
}

fn aggregate_spans(spans: &[Span]) -> Vec<SpanStats> {
    let mut by: HashMap<&str, usize> = HashMap::new();
    let mut out: Vec<SpanStats> = Vec::new();
    for sp in spans {
        let i = *by.entry(&sp.name).or_insert_with(|| {
            out.push(SpanStats {
                name: sp.name.clone(),
                action: sp
                    .attributes
                    .get("goap.action")
                    .cloned()
                    .unwrap_or_default(),
                tool: sp
                    .attributes
                    .get("gen_ai.tool.name")
                    .cloned()
                    .unwrap_or_default(),
                ..Default::default()
            });
            out.len() - 1
        });
        let s = &mut out[i];
        s.count += 1;
        s.duration_ms += sp.duration_ms;
        s.max_duration_ms = s.max_duration_ms.max(sp.duration_ms);
        if sp.error {
            s.errors += 1;
        }
    }
    out.sort_by_key(|s| std::cmp::Reverse(s.duration_ms));
    out
}

fn findings(r: &Report, th: &Thresholds) -> Vec<Finding> {
    let mut out = Vec::new();
    let el = |kind: &str, name: &str| element_key(&r.methodology, &r.version, kind, name);
    for s in &r.actions {
        let base = Finding {
            methodology: r.methodology.clone(),
            agent: s.agent.clone(),
            action: s.action.clone(),
            element: el("action", &s.action),
            records: s.records.clone(),
            ..Default::default()
        };
        if s.executions >= th.loop_executions {
            out.push(Finding {
                kind: FIND_LOOP.into(),
                count: s.executions,
                evidence: format!(
                    "{} executed {} times in the run (threshold {})",
                    s.action, s.executions, th.loop_executions
                ),
                ..base.clone()
            });
        }
        if s.failures >= th.failures {
            out.push(Finding {
                kind: FIND_FAILURE.into(),
                count: s.failures,
                evidence: format!(
                    "{}: {} executions without the promised effects out of {}",
                    s.action, s.failures, s.executions
                ),
                ..base.clone()
            });
        }
        if s.model_calls > 0 {
            let share = if r.tokens > 0 {
                s.tokens as f64 / r.tokens as f64
            } else {
                0.0
            };
            if regular(s, th) {
                let outputs: Vec<&str> = s.outputs.keys().map(String::as_str).collect();
                out.push(Finding {
                    kind: FIND_SYSTEMATIZABLE.into(),
                    tokens: s.tokens,
                    share,
                    count: s.model_calls,
                    evidence: format!(
                        "{}: {} LLM calls ({} tokens) for a regular output ({}): systematizable with a script",
                        s.action,
                        s.model_calls,
                        s.tokens,
                        outputs.join(", ")
                    ),
                    ..base.clone()
                });
            } else if s.model_calls >= th.llm_calls
                || (r.tokens > 0 && share >= th.llm_token_share && s.tokens > 0)
            {
                out.push(Finding {
                    kind: FIND_LLM_HEAVY.into(),
                    tokens: s.tokens,
                    share,
                    count: s.model_calls,
                    evidence: format!(
                        "{}: {} LLM calls, {} tokens ({:.0}% of the run)",
                        s.action,
                        s.model_calls,
                        s.tokens,
                        100.0 * share
                    ),
                    ..base.clone()
                });
            }
        }
        if s.max_duration_ms >= th.slow_action_ms {
            out.push(Finding {
                kind: FIND_SLOW_ACTION.into(),
                duration: s.max_duration_ms,
                evidence: format!("{}: up to {} ms per execution", s.action, s.max_duration_ms),
                ..base.clone()
            });
        }
    }
    if r.replans >= th.replans {
        out.push(Finding {
            kind: FIND_REPLANNING.into(),
            methodology: r.methodology.clone(),
            agent: r.agent.clone(),
            element: el("agent", &r.agent),
            count: r.replans,
            evidence: format!(
                "the plan changed {} times over {} cycles: preconditions / effects need review",
                r.replans, r.ticks
            ),
            ..Default::default()
        });
    }
    for sp in &r.spans {
        if sp.max_duration_ms < th.slow_span_ms {
            continue;
        }
        let element = if sp.action.is_empty() {
            el("methodology", "")
        } else {
            el("action", &sp.action)
        };
        out.push(Finding {
            kind: FIND_SLOW_SPAN.into(),
            methodology: r.methodology.clone(),
            agent: r.agent.clone(),
            action: sp.action.clone(),
            span: sp.name.clone(),
            duration: sp.max_duration_ms,
            count: sp.count,
            element,
            evidence: format!(
                "span {}: {} calls, up to {} ms",
                quote(&sp.name),
                sp.count,
                sp.max_duration_ms
            ),
            ..Default::default()
        });
    }
    out
}

/// regular reports whether the output of an LLM action looks systematizable: one single shape of produced items.
fn regular(s: &ActionStats, th: &Thresholds) -> bool {
    if s.kind != "llm" || s.outputs.len() != 1 {
        return false;
    }
    s.outputs.values().sum::<i64>() >= th.min_systematized
}

impl Report {
    /// WithDisabled adds findings for actions disabled in the run.
    pub fn with_disabled(&mut self, agent: &str, disabled: &[String]) {
        for a in disabled {
            self.findings.push(Finding {
                kind: FIND_DISABLED.into(),
                methodology: self.methodology.clone(),
                agent: agent.to_string(),
                action: a.clone(),
                element: element_key(&self.methodology, &self.version, "action", a),
                evidence: format!("{a} disabled after repeated failures"),
                ..Default::default()
            });
        }
    }
}
