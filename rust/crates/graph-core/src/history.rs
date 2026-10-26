//! The history of a change (ADR 0109 §4, Go's `contracts/journal` history): what the runs on the change did, step by
//! step, with the outcome of each step and whether its success still stands. It is a pure fold of the log of the
//! change, the facet the conditions, the guards, the gates and the guardians read as `history`.
use std::collections::{BTreeMap, HashMap};

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::{
    Result,
    change::Change,
    impact::{LOG_FACT, LOG_IMPACT, LogEntry},
    methodologies::{self, Declared},
    store::ReadTx,
};

/// The stream of the journal entries (`journal.StreamJournal`).
const STREAM_JOURNAL: &str = "journal";

/// The record of a step that ran to its end on the context the log had then (`journal.KindStepEnded`).
const KIND_STEP_ENDED: &str = "step.ended";
const KIND_PROCESS_STARTED: &str = "process.started";
const KIND_PROCESS_ENDED: &str = "process.ended";
const KIND_ACTION: &str = "action";

/// The outcomes of a step run.
const OUTCOME_SUCCEEDED: &str = "succeeded";
const OUTCOME_FAILED: &str = "failed";
const OUTCOME_RUNNING: &str = "running";

/// The entry types the fold reads (`journal.HistoryTypes`): the records of the runs and the steps, and the writes that
/// move what a step started from.
pub const TYPES: [&str; 6] = [
    "journal.process.started",
    "journal.process.ended",
    "journal.action",
    "journal.step.ended",
    "impact.",
    "fact.",
];

/// The operations on an impact that change what a step reading it saw (`impactMoves`): an edit of the draft, a move of
/// its lifecycle, its cancellation or withdrawal, the rebase of the version it starts from. A review or a commit does
/// not change the content.
const IMPACT_MOVES: [&str; 5] = ["updated", "transitioned", "cancelled", "withdrawn", "rebased"];

/// The fields of a journal record the fold reads (`journal.Record`).
#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase", default)]
struct Record {
    id: String,
    process_id: String,
    parent_process_id: String,
    kind: String,
    methodology: String,
    agent: String,
    goal: String,
    status: String,
    action: String,
    activity_ref: String,
    subject: String,
    subject_ref: String,
    effects_met: Option<bool>,
    error: String,
    data: Option<Map<String, Value>>,
}

/// A process that ran on the change (`journal.Run`).
#[derive(Debug, Clone, Default, Serialize)]
pub struct Run {
    pub process: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub parent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub status: String,
    pub seq: i64,
}

/// One run of a step of a process (`journal.StepRun`).
#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct StepRun {
    pub path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    pub process: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub action: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    pub outcome: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    /// The subject a scoped run was for, and what the log names it by: its inputs are that subject alone.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject_ref: String,
    /// Where in the log the run ended, and how many runs of the path there were up to this one.
    pub seq: i64,
    pub runs: i64,
    /// The run succeeded and its success still stands (ADR 0109 §5); `stale_by` says what moved otherwise.
    pub current: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub stale_by: String,
    /// The processes the step started (sub-agents), whose writes are the step's.
    #[serde(skip)]
    children: Vec<String>,
}

/// The history of a change.
#[derive(Debug, Clone, Default, Serialize)]
pub struct History {
    pub runs: Vec<Run>,
    pub steps: Vec<StepRun>,
}

impl History {
    /// The history as the conditions read it: `{runs, steps, last}`, `last` mapping each step path to its latest run.
    pub fn value(&self) -> Value {
        let runs: Vec<Value> = self
            .runs
            .iter()
            .map(|r| {
                json!({"process": r.process, "parent": r.parent, "methodology": r.methodology, "agent": r.agent,
                    "goal": r.goal, "status": r.status, "seq": r.seq})
            })
            .collect();
        let mut steps = Vec::with_capacity(self.steps.len());
        let mut last = Map::new();
        for s in &self.steps {
            let m = json!({"path": s.path, "methodology": s.methodology, "process": s.process, "action": s.action,
                "execution": s.execution, "outcome": s.outcome, "succeeded": s.outcome == OUTCOME_SUCCEEDED,
                "error": s.error, "seq": s.seq, "runs": s.runs, "current": s.current, "staleBy": s.stale_by,
                "subject": s.subject});
            steps.push(m.clone());
            last.insert(s.path.clone(), m);
        }
        json!({"runs": runs, "steps": steps, "last": last})
    }
}

/// A write that may move what a step started from: an event on an impact, or a version of a keyed item (`writeOf`).
struct Write {
    seq: i64,
    subject: String,
    execution: String,
    id: String,
}

/// Whether a step relies on an earlier step of the same methodology (`journal.Relies`): the step dependencies of each
/// methodology that ran on the change, by name.
pub type Relies = HashMap<String, BTreeMap<String, std::collections::BTreeSet<String>>>;

/// The history the log entries of a change tell (`journal.Fold`, the entries in the order of the log; the others are
/// skipped). `relies` says which steps rely on which: the writes of a step relying on another never make the other
/// stale.
pub fn fold(entries: &[LogEntry], relies: &Relies) -> History {
    let mut h = History::default();
    let mut parent: HashMap<String, String> = HashMap::new(); // process -> parent process
    let mut proc_of: HashMap<String, String> = HashMap::new(); // execution -> process
    let mut born: HashMap<String, i64> = HashMap::new(); // subject -> where it first appeared
    let mut writes: Vec<Write> = Vec::new();
    let mut ended: HashMap<String, usize> = HashMap::new(); // execution -> index of its step run
    let mut run_idx: HashMap<String, usize> = HashMap::new(); // process -> index of its run
    for e in entries {
        match e.stream() {
            STREAM_JOURNAL => {
                let Ok(r) = serde_json::from_str::<Record>(&e.payload) else {
                    continue;
                };
                if !r.parent_process_id.is_empty() {
                    parent.insert(r.process_id.clone(), r.parent_process_id.clone());
                }
                match r.kind.as_str() {
                    KIND_PROCESS_STARTED => {
                        run_idx.insert(r.process_id.clone(), h.runs.len());
                        h.runs.push(Run {
                            process: r.process_id,
                            parent: r.parent_process_id,
                            methodology: r.methodology,
                            agent: r.agent,
                            goal: r.goal,
                            status: r.status,
                            seq: e.seq,
                        });
                    }
                    KIND_PROCESS_ENDED => {
                        if let Some(i) = run_idx.get(&r.process_id) {
                            h.runs[*i].status = r.status;
                        }
                    }
                    KIND_STEP_ENDED => {
                        let exec = r
                            .data
                            .as_ref()
                            .and_then(|d| d.get("execution"))
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_string();
                        drop_running(&mut h, &r.activity_ref, &r.process_id, &mut ended);
                        ended.insert(exec.clone(), h.steps.len());
                        proc_of.insert(exec.clone(), r.process_id.clone());
                        h.steps.push(StepRun {
                            path: r.activity_ref,
                            methodology: r.methodology,
                            process: r.process_id,
                            action: r.action,
                            execution: exec,
                            outcome: OUTCOME_SUCCEEDED.into(),
                            seq: e.seq,
                            subject: r.subject,
                            subject_ref: r.subject_ref,
                            children: strings(r.data.as_ref().and_then(|d| d.get("children"))),
                            ..StepRun::default()
                        });
                    }
                    KIND_ACTION => {
                        proc_of.insert(r.id.clone(), r.process_id.clone());
                        if r.activity_ref.is_empty() {
                            continue;
                        }
                        let progress =
                            r.data.as_ref().and_then(|d| d.get("progress")).and_then(Value::as_bool) == Some(true);
                        let failed = !r.error.is_empty() || (r.effects_met == Some(false) && !progress);
                        if let Some(i) = ended.get(&r.id).copied() {
                            if failed {
                                h.steps[i].outcome = OUTCOME_FAILED.into();
                                h.steps[i].error = r.error;
                                h.steps[i].seq = e.seq;
                            }
                            continue;
                        }
                        let outcome = if failed { OUTCOME_FAILED } else { OUTCOME_RUNNING };
                        drop_running(&mut h, &r.activity_ref, &r.process_id, &mut ended);
                        h.steps.push(StepRun {
                            path: r.activity_ref,
                            methodology: r.methodology,
                            process: r.process_id,
                            action: r.action,
                            execution: r.id,
                            outcome: outcome.into(),
                            error: r.error,
                            seq: e.seq,
                            subject: r.subject,
                            subject_ref: r.subject_ref,
                            ..StepRun::default()
                        });
                    }
                    _ => {}
                }
            }
            LOG_IMPACT => {
                born.entry(e.subject.clone()).or_insert(e.seq);
                let op = e.typ.strip_prefix(LOG_IMPACT).map(|o| o.trim_start_matches('.'));
                if op.is_some_and(|op| IMPACT_MOVES.contains(&op)) {
                    writes.push(Write {
                        seq: e.seq,
                        subject: e.subject.clone(),
                        execution: e.execution.clone(),
                        id: e.id.clone(),
                    });
                }
            }
            LOG_FACT => {
                #[derive(Deserialize)]
                struct Item {
                    #[serde(default)]
                    r#type: String,
                    #[serde(default)]
                    data: Map<String, Value>,
                }
                let Ok(it) = serde_json::from_str::<Item>(&e.payload) else {
                    continue;
                };
                let key = it.data.get("key").and_then(Value::as_str).unwrap_or_default();
                if key.is_empty() {
                    continue; // an item without key is never rewritten: a new one moves nothing a step saw
                }
                let subject = format!("{}:{key}", it.r#type);
                if let std::collections::hash_map::Entry::Vacant(slot) = born.entry(subject.clone()) {
                    slot.insert(e.seq);
                    continue;
                }
                writes.push(Write {
                    seq: e.seq,
                    subject,
                    execution: e.execution.clone(),
                    id: e.id.clone(),
                });
            }
            _ => {}
        }
    }
    // a running step whose action is not over yet stays running; count the runs of each path
    let mut count: HashMap<String, i64> = HashMap::new();
    for s in &mut h.steps {
        let n = count.entry(s.path.clone()).or_default();
        *n += 1;
        s.runs = *n;
    }
    current(&mut h, &parent, &proc_of, &born, &writes, relies);
    h
}

fn strings(v: Option<&Value>) -> Vec<String> {
    v.and_then(Value::as_array)
        .map(|l| l.iter().filter_map(Value::as_str).map(str::to_string).collect())
        .unwrap_or_default()
}

/// Removes the running entry of a step a process runs again (`dropRunning`): a step waiting for its sub-agent is run
/// again once the sub-agent ends, its first run never ended.
fn drop_running(h: &mut History, path: &str, process: &str, ended: &mut HashMap<String, usize>) {
    for i in (0..h.steps.len()).rev() {
        let s = &h.steps[i];
        if s.path != path || s.process != process {
            continue;
        }
        if !s.subject.is_empty() && s.outcome != OUTCOME_RUNNING {
            return; // a scoped step runs once per subject: an earlier subject's run is not run again
        }
        if s.outcome == OUTCOME_RUNNING {
            h.steps.remove(i);
            for j in ended.values_mut() {
                if *j > i {
                    *j -= 1;
                }
            }
        }
        return;
    }
}

/// Sets `current` and `stale_by` on every step run (`History.current`, ADR 0109 §5): a success stands while (1) nothing
/// it started from moved after it, but by a step relying on it, and (2) every step it relies on has a success standing,
/// older than its own.
fn current(
    h: &mut History,
    parent: &HashMap<String, String>,
    proc_of: &HashMap<String, String>,
    born: &HashMap<String, i64>,
    writes: &[Write],
    relies: &Relies,
) {
    // the step run a process belongs to: the step whose sub-agents it descends from
    let mut owner: HashMap<String, usize> = HashMap::new();
    for (i, s) in h.steps.iter().enumerate() {
        for c in &s.children {
            owner.insert(c.clone(), i);
        }
    }
    let step_of = |execution: &str| -> Option<usize> {
        let mut p = proc_of.get(execution).map(String::as_str).unwrap_or_default();
        while !p.is_empty() {
            if let Some(i) = owner.get(p) {
                return Some(*i);
            }
            p = parent.get(p).map(String::as_str).unwrap_or_default();
        }
        h.steps.iter().position(|s| s.execution == execution)
    };
    // the step each write belongs to, read before anything is set
    let of_write: Vec<Option<usize>> = writes.iter().map(|w| step_of(&w.execution)).collect();
    let rel = |later: &StepRun, earlier: &StepRun| -> bool {
        later.methodology == earlier.methodology
            && relies
                .get(&later.methodology)
                .and_then(|d| d.get(&later.path))
                .is_some_and(|on| on.contains(&earlier.path))
    };
    let mut latest: BTreeMap<String, usize> = BTreeMap::new(); // path -> its latest run, in log order
    for i in 0..h.steps.len() {
        latest.insert(h.steps[i].path.clone(), i);
        if h.steps[i].outcome != OUTCOME_SUCCEEDED {
            continue;
        }
        h.steps[i].current = true;
        // (1) a write after it, on what existed before it ended, by nothing relying on it
        for (k, w) in writes.iter().enumerate() {
            let s = &h.steps[i];
            if w.seq <= s.seq
                || born.get(&w.subject).copied().unwrap_or_default() >= s.seq
                || (!s.subject_ref.is_empty() && w.subject != s.subject_ref)
            {
                continue;
            }
            if let Some(j) = of_write[k]
                && (j == i || rel(&h.steps[j], s))
            {
                continue;
            }
            let by = format!("log entry {}", w.id);
            let s = &mut h.steps[i];
            (s.current, s.stale_by) = (false, by);
            break;
        }
    }
    // (2) in log order: what it relies on stands and is older; a later run of a step it relies on makes it stale
    for i in 0..h.steps.len() {
        if !h.steps[i].current {
            continue;
        }
        for (path, j) in &latest {
            let (s, p) = (&h.steps[i], &h.steps[*j]);
            if *path == s.path || !rel(s, p) {
                continue;
            }
            if p.seq > s.seq || (p.outcome == OUTCOME_SUCCEEDED && !p.current) || p.outcome == OUTCOME_FAILED {
                let by = format!("step {path}");
                let s = &mut h.steps[i];
                (s.current, s.stale_by) = (false, by);
                break;
            }
        }
    }
    // a stale step makes the steps relying on it stale: repeat until nothing moves (the chain is finite)
    let mut changed = true;
    while changed {
        changed = false;
        for i in 0..h.steps.len() {
            if !h.steps[i].current {
                continue;
            }
            let stale = latest.iter().find(|(path, j)| {
                **path != h.steps[i].path && rel(&h.steps[i], &h.steps[**j]) && !h.steps[**j].current
            });
            if let Some((path, _)) = stale {
                let by = format!("step {path}");
                let s = &mut h.steps[i];
                (s.current, s.stale_by) = (false, by);
                changed = true;
            }
        }
    }
}

/// The history facet of the blackboard of a change, as the graph folds it (`graphsvc.LogFacets` with
/// `journal.FoldWith`): the entries of its log the fold reads, with the step dependencies of each methodology that ran
/// on it (the change's own at the version it follows, ADR 0113).
pub async fn of<T: ReadTx>(tx: &T, c: &Change) -> Result<Value> {
    let prefixes: Vec<&str> = TYPES.to_vec();
    let entries = tx.log(&c.id, 0, &prefixes).await?;
    let mut relies = Relies::new();
    let mut declared: Option<Declared> = None;
    for name in methodologies_of(&entries) {
        let d = match &declared {
            Some(d) => d,
            None => declared.insert(Declared::read(tx).await?),
        };
        let reference = if name == c.methodology {
            methodologies::reference(&c.methodology, &c.methodology_version)
        } else {
            name.clone()
        };
        if let Some(i) = d.instance(tx, &reference).await? {
            relies.insert(name, i.derived.step_deps);
        }
    }
    Ok(fold(&entries, &relies).value())
}

/// The methodologies the runs of a log name, in the order they appear.
fn methodologies_of(entries: &[LogEntry]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for e in entries.iter().filter(|e| e.stream() == STREAM_JOURNAL) {
        if let Ok(r) = serde_json::from_str::<Record>(&e.payload)
            && !r.methodology.is_empty()
            && !out.contains(&r.methodology)
        {
            out.push(r.methodology);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::Utc;

    /// The logs the Go tests fold, with the history Go folds from each (`contracts/journal`
    /// `TestRustHistoryCasesAreCurrent`).
    #[test]
    fn folds_the_history_go_folds() {
        let cases: Value = serde_json::from_str(include_str!("history_cases.gen.json")).unwrap();
        for c in cases["cases"].as_array().unwrap() {
            let entries: Vec<LogEntry> = c["log"]
                .as_array()
                .unwrap()
                .iter()
                .map(|e| LogEntry {
                    seq: e["seq"].as_i64().unwrap(),
                    id: e["id"].as_str().unwrap().into(),
                    typ: e["type"].as_str().unwrap().into(),
                    subject: e["subject"].as_str().unwrap().into(),
                    execution: e["execution"].as_str().unwrap().into(),
                    payload: e["payload"].as_str().unwrap().into(),
                    change: crate::model::ChangeId::default(),
                    flow: String::new(),
                    process: String::new(),
                    by: String::new(),
                    at: Utc::now(),
                    causation: String::new(),
                })
                .collect();
            let relies: Relies = serde_json::from_value(c["relies"].clone()).unwrap();
            let got = serde_json::to_value(fold(&entries, &relies)).unwrap();
            // a Go slice with nothing in it is null
            let mut want = c["history"].clone();
            for k in ["runs", "steps"] {
                if want[k].is_null() {
                    want[k] = json!([]);
                }
            }
            assert_eq!(got, want, "{}", c["name"]);
        }
    }
}
