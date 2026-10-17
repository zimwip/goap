//! Scoped actions (ADR 0109 §3.5, Go's `scoped.go`): an action of a scope runs once per pending subject, one subject
//! per step, the planner seeing it lifted. The engine picks the subject, checks the effects on it, and gives a subject
//! up once it failed (Engine::max_failures).

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;

use goap_condition::{SCOPE_ACTION, SCOPE_IMPACT, SCOPE_ITEM, SCOPE_RISK};
use goap_ctx::Ctx;
use goap_domain::Blackboard;
use goap_methodology::{Action, Compiled, KIND_LLM, KIND_TOOL};
use serde_json::{Map, Value};

use crate::cycle::new_step;
use crate::engine::{Engine, Ran};
use crate::error::{Error, Result};
use crate::executors::ActionContext;
use crate::journal::action_record;
use crate::process::{LLMCall, Process, Step};

impl Engine {
    pub(crate) fn max_failures_or_default(&self) -> i64 {
        if self.max_failures == 0 {
            1
        } else {
            self.max_failures
        }
    }

    /// pending_subjects returns up to n subjects the scoped action is pending for at the last observation, in the
    /// order of its scope, leaving out those it failed on too often in this process.
    pub(crate) fn pending_subjects(&self, p: &Process, a: &Action, n: usize) -> Vec<String> {
        let mut failures: HashMap<&str, i64> = HashMap::new();
        for s in &p.steps {
            if s.action == a.name
                && !s.subject.is_empty()
                && !s.effects_met
                && !s.progress
                && s.ended_at.is_some()
            {
                *failures.entry(s.subject.as_str()).or_default() += 1;
            }
        }
        let cond = a.subject_condition();
        let mut out = Vec::new();
        for sub in p
            .subjects
            .get(&a.scope)
            .map(Vec::as_slice)
            .unwrap_or_default()
        {
            if out.len() == n {
                break;
            }
            if sub.values.get(&cond).copied().unwrap_or(false)
                && failures.get(sub.id.as_str()).copied().unwrap_or(0)
                    < self.max_failures_or_default()
            {
                out.push(sub.id.clone());
            }
        }
        out
    }

    /// parallelism is the number of subjects a scoped action runs for at once.
    pub(crate) fn parallelism(&self, a: &Action) -> usize {
        if a.kind != KIND_LLM && a.kind != KIND_TOOL {
            return 1;
        }
        let mut n = if self.max_parallel <= 0 {
            4
        } else {
            self.max_parallel
        };
        if a.parallel > 0 && a.parallel < n {
            n = a.parallel;
        }
        n as usize
    }

    /// run_scoped runs a scoped action for several pending subjects at once: one step per subject, their executors
    /// called concurrently, then what each returned settled on the process one after the other.
    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn run_scoped(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
        bb: Blackboard,
        action: &Action,
        calls: &[LLMCall],
        ids: Vec<String>,
    ) -> Result<()> {
        struct Job {
            i: usize,
            bb: Blackboard,
            imp: Action,
            spec_err: Option<Error>,
        }
        let mut jobs: Vec<Job> = Vec::new();
        let mut permission = String::new();
        for (n, id) in ids.iter().enumerate() {
            let planning: &[LLMCall] = if n == 0 { calls } else { &[] };
            let mut step = new_step(p, &bb, action, planning, self.clock());
            let sbb = bind_subject(bb.clone(), action, id, &mut step);
            let (imp, spec_err) = match self.specialize(ctx, p, m, action, &sbb).await {
                Ok((imp, spec)) => {
                    if !spec.is_empty() {
                        step.specialization = spec;
                    }
                    (imp, None)
                }
                Err(e) => (action.clone(), Some(e)),
            };
            let perm = if spec_err.is_none() && !step.specialization.is_empty() {
                imp.permission.clone()
            } else {
                action.permission.clone()
            };
            if n == 0 {
                if self.gate_action(ctx, p, m, action, &step, &perm).await? {
                    return Ok(());
                }
                permission = perm;
            } else if perm != permission {
                break;
            }
            step.execution = uuid::Uuid::new_v4().to_string();
            p.steps.push(step);
            jobs.push(Job {
                i: p.steps.len() - 1,
                bb: sbb,
                imp,
                spec_err,
            });
        }
        self.checkpoint(ctx, p).await?;
        let snapshot = Arc::new(p.clone());
        let runs = futures::future::join_all(jobs.iter().map(|j| {
            let snapshot = snapshot.clone();
            async move {
                if j.spec_err.is_some() {
                    return None;
                }
                Some(
                    self.run_action_on(ctx, snapshot, m, &j.bb, &j.imp, j.i)
                        .await,
                )
            }
        }))
        .await;
        let mut first: Option<Error> = None;
        for (j, run) in jobs.into_iter().zip(runs) {
            let mut kind = j.imp.kind.clone();
            let err = match (j.spec_err, run) {
                (Some(e), _) => {
                    p.steps[j.i].error = e.to_string();
                    p.steps[j.i].ended_at = Some(self.clock());
                    self.record_failure(p, &action.name);
                    kind = action.kind.clone();
                    None
                }
                (None, Some(Err(e))) => Some(e),
                (None, Some(Ok((rctx, end, host, sc, res, err)))) => {
                    p.steps[j.i].span_id = self.span_id(&rctx);
                    let r = Ran {
                        ctx: rctx,
                        end,
                        host,
                        sc,
                        res,
                        err,
                    };
                    self.settle_action(p, m, &j.imp, j.i, r).await.err()
                }
                (None, None) => None,
            };
            let execution = p.steps[j.i].execution.clone();
            let rec = action_record(p, j.i, &kind, &action.step, &execution);
            self.journal(ctx, p, vec![rec]).await;
            p.steps[j.i].drop_exchanges();
            if let Some(e) = err
                && first.is_none()
            {
                first = Some(e);
            }
        }
        match first {
            Some(e) => Err(e),
            None => Ok(()),
        }
    }

    /// disable takes an action out of the plans of the process.
    pub(crate) fn disable(&self, p: &mut Process, action: &str, why: &str) {
        p.disabled.insert(action.to_string(), true);
        tracing::warn!(process = %p.id, action, reason = why, "action disabled");
    }
}

/// bind_subject gives the blackboard of a run of a scoped action its subject (`vars.item`, `vars.subject`) and
/// records the subject on the step.
pub(crate) fn bind_subject(
    mut bb: Blackboard,
    a: &Action,
    id: &str,
    step: &mut Step,
) -> Blackboard {
    step.subject = id.to_string();
    let entry = goap_condition::subject_entry(&bb, &a.scope, id);
    bb.vars.insert(
        "item".into(),
        entry.clone().map(Value::Object).unwrap_or(Value::Null),
    );
    bb.vars
        .insert("subject".into(), Value::String(id.to_string()));
    step.subject_ref = subject_ref(&a.scope, entry.as_ref());
    bb
}

/// subject_holds reports whether the scoped effects of the action hold on the subject.
pub(crate) fn subject_holds(p: &Process, a: &Action, id: &str) -> bool {
    for sub in p
        .subjects
        .get(&a.scope)
        .map(Vec::as_slice)
        .unwrap_or_default()
    {
        if sub.id == id {
            return sub.holds(&a.scoped_effects);
        }
    }
    // the subject is gone (an impact withdrawn): nothing is left to do for it
    true
}

/// change_effects are the effects of the change of a scoped action, without the aggregates its scoped effects lift
/// to.
pub(crate) fn change_effects(a: &Action) -> BTreeMap<String, bool> {
    a.effects
        .iter()
        .filter(|(k, _)| !a.lifted.get(*k).copied().unwrap_or(false))
        .map(|(k, v)| (k.clone(), *v))
        .collect()
}

/// subject_ref is what the log of the change names a subject by.
pub(crate) fn subject_ref(scope: &str, entry: Option<&Map<String, Value>>) -> String {
    let Some(entry) = entry else {
        return String::new();
    };
    let s = |m: &Map<String, Value>, k: &str| {
        m.get(k)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    match scope {
        SCOPE_IMPACT => s(entry, "id"),
        SCOPE_ITEM => {
            let key = entry
                .get("data")
                .and_then(Value::as_object)
                .map(|d| s(d, "key"))
                .unwrap_or_default();
            if !key.is_empty() {
                return format!("{}:{key}", s(entry, "type"));
            }
            s(entry, "id")
        }
        SCOPE_RISK | SCOPE_ACTION => format!("{scope}:{}", s(entry, "key")),
        _ => String::new(),
    }
}

/// action_vars are the variables an action sees: the process's, plus `item` and `subject` for a scoped run, and
/// `form` for the code of a human action.
pub(crate) fn action_vars(ac: &ActionContext) -> Map<String, Value> {
    let mut out = ac.process.vars.clone();
    if ac.action.is_scoped() {
        out.insert(
            "item".into(),
            ac.blackboard
                .vars
                .get("item")
                .cloned()
                .unwrap_or(Value::Null),
        );
        out.insert(
            "subject".into(),
            ac.blackboard
                .vars
                .get("subject")
                .cloned()
                .unwrap_or(Value::Null),
        );
    }
    if let Some(f) = &ac.form {
        out.insert("form".into(), Value::Object(f.clone()));
    }
    out
}

/// counted is the number of steps the step limit of a process counts: the runs of a scoped action for its subjects
/// count once per action.
pub(crate) fn counted(steps: &[Step]) -> i64 {
    let mut n = 0;
    let mut scoped = HashSet::new();
    for s in steps {
        if s.subject.is_empty() || scoped.insert(s.action.as_str()) {
            n += 1;
        }
    }
    n
}
