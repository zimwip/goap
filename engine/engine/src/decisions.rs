//! Decision loops (ADR 0009 §4, Go's `decisions.go`): an action works on the decision points of its change through
//! items of kind "decisionPoint": open a point, rule it, answer a question, ratify a ruling. The builtin
//! decision.investigate answers an open question by running a sub-agent whose intent is the question.

use goap_ctx::Ctx;
use goap_domain::{
    DECISION_ANSWER_OP, DECISION_OPEN_OP, DECISION_RATIFY_OP, DECISION_RULE_OP, DecisionPoint,
};
use goap_graphapi::{OpenDecisionRequest, RuleRequest};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::engine::Engine;
use crate::error::{Error, Result};
use crate::executors::{ActionContext, ActionResult, ExecResult};
use crate::host::Host;
use crate::process::{Process, STATUS_COMPLETED};
use crate::serde_util::{is_false, null_default};

/// DecisionOp is the operation of a "decisionPoint" item.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct DecisionOp {
    /// open, rule, answer or ratify.
    #[serde(deserialize_with = "null_default")]
    pub op: String,
    /// The point an open creates, for the operations of the same batch ("#ref").
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub r#ref: String,
    /// The id of a point, or "#ref" of one opened in the batch; empty: the only pending point.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub point: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub question: String,
    /// Option ids or names; none: the open options of the change.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub options: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub criteria: Vec<String>,
    /// The policy values of the point, passed on without being read.
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub policy: Map<String, Value>,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub outcome: String,
    /// Option id or name.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub option: String,
    #[serde(skip_serializing_if = "is_zero_f64")]
    pub confidence: f64,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub justification: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub questions: Vec<String>,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub question_id: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub answer: String,
    #[serde(skip_serializing_if = "is_false")]
    pub accept: bool,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub comment: String,
}

fn is_zero_f64(v: &f64) -> bool {
    *v == 0.0
}

impl Engine {
    /// apply_decision_ops applies the decision operations of an action, in order, and returns the decision points
    /// they worked on. human tells they come from a person.
    pub(crate) async fn apply_decision_ops(
        &self,
        ctx: &Ctx,
        p: &Process,
        ops: Vec<DecisionOp>,
        human: bool,
        by: &str,
    ) -> Result<Vec<String>> {
        if ops.is_empty() {
            return Ok(Vec::new());
        }
        if p.change_id.is_empty() {
            return Err(Error::msg(format!(
                "process {} has no change attached: call goap-scheduler/attach first (ADR 0031)",
                p.id
            )));
        }
        let bb = self
            .graph
            .blackboard_in(ctx, &p.change_id, goap_domain::MAIN_FLOW)
            .await?;
        let opts = goap_domain::options_of(&bb);
        let option = |s: &str| -> String {
            for o in &opts {
                if o.id == s
                    || o.option
                        .as_ref()
                        .is_some_and(|x| x.name.eq_ignore_ascii_case(s))
                {
                    return o.id.clone();
                }
            }
            s.to_string()
        };
        let mut points: Vec<String> = Vec::new();
        let touched = |points: &mut Vec<String>, d: &DecisionPoint| {
            if !points.contains(&d.id) {
                points.push(d.id.clone());
            }
        };
        let mut refs: std::collections::HashMap<String, String> = std::collections::HashMap::new();
        for (i, op) in ops.iter().enumerate() {
            let point = |s: &str| -> String {
                match refs.get(s.trim_start_matches('#')) {
                    Some(id) if !s.is_empty() => id.clone(),
                    _ => s.to_string(),
                }
            };
            let res: Result<DecisionPoint> = match op.op.as_str() {
                DECISION_OPEN_OP => {
                    let options: Vec<String> = op.options.iter().map(|o| option(o)).collect();
                    let req = OpenDecisionRequest {
                        question: op.question.clone(),
                        options: if options.is_empty() {
                            None
                        } else {
                            Some(options)
                        },
                        criteria: op.criteria.clone(),
                        policy: op.policy.clone(),
                        by: by.to_string(),
                    };
                    let r = self
                        .graph
                        .open_decision(ctx, &p.change_id, req)
                        .await
                        .map_err(Error::from);
                    if let Ok(d) = &r
                        && !op.r#ref.is_empty()
                    {
                        refs.insert(op.r#ref.trim_start_matches('#').to_string(), d.id.clone());
                    }
                    r
                }
                DECISION_RULE_OP => {
                    let req = RuleRequest {
                        point: point(&op.point),
                        outcome: op.outcome.clone(),
                        option: if op.option.is_empty() {
                            String::new()
                        } else {
                            option(&op.option)
                        },
                        confidence: op.confidence,
                        justification: op.justification.clone(),
                        questions: op.questions.clone(),
                        human,
                        by: by.to_string(),
                    };
                    self.graph
                        .rule_decision(ctx, &p.change_id, req)
                        .await
                        .map_err(Error::from)
                }
                DECISION_ANSWER_OP => self
                    .graph
                    .answer_question(ctx, &p.change_id, &op.question_id, &op.answer, "", by)
                    .await
                    .map_err(Error::from),
                DECISION_RATIFY_OP => {
                    if !human {
                        Err(Error::msg(
                            "a ruling is ratified by a person: a human task, not an agent",
                        ))
                    } else {
                        self.graph
                            .ratify_decision(
                                ctx,
                                &p.change_id,
                                &point(&op.point),
                                op.accept,
                                by,
                                &op.comment,
                            )
                            .await
                            .map_err(Error::from)
                    }
                }
                other => Err(Error::msg(format!(
                    "unknown decision operation {other:?}: open, rule, answer or ratify"
                ))),
            };
            match res {
                Ok(d) => touched(&mut points, &d),
                Err(err) => return Err(err.context(format!("decision operation {i} ({})", op.op))),
            }
        }
        Ok(points)
    }

    /// run_investigation runs (or resumes) the sub-agent of an investigation: identification may pick an agent of any
    /// methodology when methodology is empty (ADR 0009 §4).
    async fn run_investigation(
        self: &std::sync::Arc<Self>,
        ctx: &Ctx,
        h: &Host,
        key: &str,
        methodology: &str,
        agent: &str,
        intent: &str,
    ) -> Result<goap_dsl::AgentResult> {
        let parent = h.process.clone();
        let mut methodology = methodology.to_string();
        if !parent.children.contains_key(key) && methodology.is_empty() && !agent.is_empty() {
            methodology = parent.methodology.clone(); // an agent is named in a methodology
        }
        self.run_child_step(ctx, h, key, &methodology, agent, "", intent, None)
            .await
    }
}

/// investigate is the builtin decision.investigate (ADR 0009 §4): it answers the open questions of the decision points
/// of the change, one sub-agent per question, whose intent is the question. Params: methodology, agent, max.
pub async fn investigate(ctx: &Ctx, ac: &ActionContext) -> ExecResult {
    let Some(h) = ac.host.clone().filter(|_| !ac.process.change_id.is_empty()) else {
        return Err(Error::msg("decision.investigate needs a process on a change").into());
    };
    let e = h.engine();
    let param = |k: &str| {
        ac.action
            .params
            .get(k)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    let (methodology, agent) = (param("methodology"), param("agent"));
    let limit: i64 = match ac.action.params.get("max") {
        Some(Value::Number(n)) => n
            .as_i64()
            .unwrap_or_else(|| n.as_f64().unwrap_or(-1.0) as i64),
        _ => -1,
    };
    let mut answered = 0i64;
    for d in goap_domain::decision_points_of(&ac.blackboard) {
        for q in &d.questions {
            if q.status != goap_domain::QUESTION_OPEN || (limit >= 0 && answered >= limit) {
                continue;
            }
            let intent = format!("{}\n\n(to decide: {})", q.text, d.question);
            let res = e
                .run_investigation(
                    &e.as_(ctx, &ac.process),
                    &h,
                    &format!("{}#{}", ac.action.name, q.id),
                    &methodology,
                    &agent,
                    &intent,
                )
                .await;
            let (answer, process) = match &res {
                Err(err) if err.is_suspended() => {
                    return Ok(ActionResult {
                        suspended: true,
                        child: h.waiting_on(),
                        output: format!("investigating {:?}", q.text),
                        ..Default::default()
                    });
                }
                Err(err) => (
                    format!("the investigation could not run: {err}"),
                    String::new(),
                ),
                Ok(r) if r.status == STATUS_COMPLETED => (
                    format!(
                        "investigated by process {}: goal {:?} reached; its findings are on the blackboard of the change",
                        r.process_id, r.goal
                    ),
                    r.process_id.clone(),
                ),
                Ok(r) => (
                    format!(
                        "investigated by process {}: it ended {} without an answer",
                        r.process_id, r.status
                    ),
                    r.process_id.clone(),
                ),
            };
            ac.graph
                .answer_question(
                    ctx,
                    &ac.process.change_id,
                    &q.id,
                    &answer,
                    &process,
                    &ac.action.name,
                )
                .await
                .map_err(Error::from)?;
            answered += 1;
        }
    }
    Ok(ActionResult {
        output: format!("{answered} question(s) answered"),
        ..Default::default()
    })
}
