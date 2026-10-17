//! The history of a change (ADR 0109 §4): what the runs on the change did, step by step, with the outcome of each step
//! and whether its success still stands. It is a pure fold of the log of the change, a part of the context the
//! conditions are derived from (the blackboard facet domain::FACET_HISTORY).

use std::{
    collections::{BTreeMap, HashMap},
    sync::Arc,
};

use goap_ctx::Ctx;
use goap_domain::{
    Change, IMPACT_CANCELLED, IMPACT_REBASED, IMPACT_TRANSITIONED, IMPACT_UPDATED,
    IMPACT_WITHDRAWN, ImpactOp, LOG_FACT, LOG_IMPACT, LogEntry,
};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{Error, KIND_ACTION, KIND_PROCESS_ENDED, KIND_PROCESS_STARTED, Record, STREAM_JOURNAL};

/// KindStepEnded records that the action of a step of a process ended without error (ADR 0109): the step ran to its
/// end on the context the log had then. It is written before the run's world is observed again, so the conditions of
/// that observation see it; the action record follows with the effects that observation found.
pub const KIND_STEP_ENDED: &str = "step.ended";

// Outcomes of a step run.
pub const OUTCOME_SUCCEEDED: &str = "succeeded";
pub const OUTCOME_FAILED: &str = "failed";
pub const OUTCOME_RUNNING: &str = "running";

/// HistoryTypes are the entry types the fold reads: the journal records of the runs and the steps, and the writes that
/// move what a step started from (impact events, facts).
pub const HISTORY_TYPES: &[&str] = &[
    "journal.process.started",
    "journal.process.ended",
    "journal.action",
    "journal.step.ended",
    "impact.",
    "fact.",
];

/// HISTORY_TYPES as the types of a domain::LogFilter.
pub fn history_types() -> Vec<String> {
    HISTORY_TYPES.iter().map(|s| s.to_string()).collect()
}

/// Run is a process that ran on the change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
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

/// StepRun is one run of a step of a process.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct StepRun {
    /// Path is the path of the step (process/step...), Methodology the methodology of the process that ran it.
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
    /// Subject is the subject a scoped run was for (ADR 0109 §3.5); its inputs are that subject alone (SubjectRef,
    /// what the log names it by).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject_ref: String,
    /// Seq is the position in the log where the run ended (succeeded or failed); Runs counts the runs of the path up
    /// to this one.
    pub seq: i64,
    pub runs: i64,
    /// Current: the run succeeded and its success still stands (ADR 0109 §5); StaleBy says why not when it succeeded:
    /// the log entry that moved what it started from, or the step it relied on that ran again.
    pub current: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub stale_by: String,
    /// children are the processes the step started (sub-agents), whose writes are the step's.
    #[serde(skip)]
    pub children: Vec<String>,
}

/// History is the history of a change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct History {
    pub runs: Vec<Run>,
    pub steps: Vec<StepRun>,
}

impl History {
    /// Last is the latest run of each step path.
    pub fn last(&self) -> BTreeMap<String, StepRun> {
        self.steps
            .iter()
            .map(|s| (s.path.clone(), s.clone()))
            .collect()
    }
}

/// Relies tells whether the step `later` relies on the step `earlier` of the same methodology (its entry needs the
/// exit of earlier, directly or through other steps): what the methodology says, given to the fold by its caller.
/// None: no step relies on another. Arguments: methodology, later, earlier.
pub type Relies<'a> = &'a dyn Fn(&str, &str, &str) -> bool;

/// writeOf is a write that may move what a step started from: an event on an impact, or a version of a keyed item.
struct WriteOf {
    seq: i64,
    /// impact id, or "<type>:<key>"
    subject: String,
    execution: String,
    id: String,
}

/// impactMoves are the operations on an impact that change what a step reading it saw: an edit of the draft, a move
/// of its lifecycle, its cancellation or withdrawal, the rebase of the version it starts from (ADR 0109 §5). A review,
/// a commit or a landing does not change the content.
const IMPACT_MOVES: [ImpactOp; 5] = [
    IMPACT_UPDATED,
    IMPACT_TRANSITIONED,
    IMPACT_CANCELLED,
    IMPACT_WITHDRAWN,
    IMPACT_REBASED,
];

fn data_str(d: &Map<String, Value>, k: &str) -> String {
    d.get(k)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

/// Fold is the history of the change whose log entries are given (in the order of the log; the other entries are
/// skipped). relies says which steps rely on which (the writes of a step relying on another never make the other
/// stale).
pub fn fold(entries: &[LogEntry], relies: Option<Relies<'_>>) -> Result<History, Error> {
    let mut h = History::default();
    let mut parent: HashMap<String, String> = HashMap::new(); // process -> parent process
    let mut proc_of: HashMap<String, String> = HashMap::new(); // execution -> process
    let mut born: HashMap<String, i64> = HashMap::new(); // subject -> seq of its first appearance
    let mut writes: Vec<WriteOf> = Vec::new();
    let mut ended: HashMap<String, usize> = HashMap::new(); // execution -> index of its step run
    let mut run_idx: HashMap<String, usize> = HashMap::new(); // process -> index of its run
    for e in entries {
        match e.stream() {
            STREAM_JOURNAL => {
                let r: Record = serde_json::from_value(e.payload.clone())?;
                if !r.parent_process_id.is_empty() {
                    parent.insert(r.process_id.clone(), r.parent_process_id.clone());
                }
                match r.kind.as_str() {
                    KIND_PROCESS_STARTED => {
                        run_idx.insert(r.process_id.clone(), h.runs.len());
                        h.runs.push(Run {
                            process: r.process_id.clone(),
                            parent: r.parent_process_id.clone(),
                            methodology: r.methodology.clone(),
                            agent: r.agent.clone(),
                            goal: r.goal.clone(),
                            status: r.status.clone(),
                            seq: e.seq,
                        });
                    }
                    KIND_PROCESS_ENDED => {
                        if let Some(&i) = run_idx.get(&r.process_id) {
                            h.runs[i].status = r.status.clone();
                        }
                    }
                    KIND_STEP_ENDED => {
                        let exec = data_str(&r.data, "execution");
                        h.drop_running(&r.activity_ref, &r.process_id, &mut ended);
                        ended.insert(exec.clone(), h.steps.len());
                        proc_of.insert(exec.clone(), r.process_id.clone());
                        h.steps.push(StepRun {
                            path: r.activity_ref.clone(),
                            methodology: r.methodology.clone(),
                            process: r.process_id.clone(),
                            action: r.action.clone(),
                            execution: exec,
                            outcome: OUTCOME_SUCCEEDED.into(),
                            seq: e.seq,
                            subject: r.subject.clone(),
                            subject_ref: r.subject_ref.clone(),
                            children: string_list(r.data.get("children")),
                            ..Default::default()
                        });
                    }
                    KIND_ACTION => {
                        proc_of.insert(r.id.clone(), r.process_id.clone());
                        if r.activity_ref.is_empty() {
                            continue;
                        }
                        let failed = !r.error.is_empty()
                            || (r.effects_met == Some(false)
                                && r.data.get("progress") != Some(&Value::Bool(true)));
                        if let Some(&i) = ended.get(&r.id) {
                            if failed {
                                let s = &mut h.steps[i];
                                s.outcome = OUTCOME_FAILED.into();
                                s.error = r.error.clone();
                                s.seq = e.seq;
                            }
                            continue;
                        }
                        let outcome = if failed {
                            OUTCOME_FAILED
                        } else {
                            OUTCOME_RUNNING
                        };
                        h.drop_running(&r.activity_ref, &r.process_id, &mut ended);
                        h.steps.push(StepRun {
                            path: r.activity_ref.clone(),
                            methodology: r.methodology.clone(),
                            process: r.process_id.clone(),
                            action: r.action.clone(),
                            execution: r.id.clone(),
                            outcome: outcome.into(),
                            error: r.error.clone(),
                            seq: e.seq,
                            subject: r.subject.clone(),
                            subject_ref: r.subject_ref.clone(),
                            ..Default::default()
                        });
                    }
                    _ => {}
                }
            }
            LOG_IMPACT => {
                born.entry(e.subject.clone()).or_insert(e.seq);
                let op = e.r#type.get(LOG_IMPACT.len() + 1..).unwrap_or_default();
                if IMPACT_MOVES.iter().any(|m| m == op) {
                    writes.push(WriteOf {
                        seq: e.seq,
                        subject: e.subject.clone(),
                        execution: e.execution.clone(),
                        id: e.id.clone(),
                    });
                }
            }
            LOG_FACT => {
                #[derive(Deserialize)]
                struct Fact {
                    #[serde(default)]
                    r#type: Option<String>,
                    #[serde(default)]
                    data: Option<Map<String, Value>>,
                }
                let Ok(it) = serde_json::from_value::<Fact>(e.payload.clone()) else {
                    continue;
                };
                let key = it
                    .data
                    .as_ref()
                    .map(|d| data_str(d, "key"))
                    .unwrap_or_default();
                if key.is_empty() {
                    continue; // an item without key is never rewritten: a new one moves nothing a step saw
                }
                let subject = format!("{}:{key}", it.r#type.unwrap_or_default());
                if let std::collections::hash_map::Entry::Vacant(v) = born.entry(subject.clone()) {
                    v.insert(e.seq);
                    continue;
                }
                writes.push(WriteOf {
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
    h.current(&parent, &proc_of, &born, &writes, relies);
    Ok(h)
}

impl History {
    /// dropRunning removes the running entry of a step a process runs again: a step waiting for its sub-agent is run
    /// again once the sub-agent ends, its first run never ended.
    fn drop_running(&mut self, path: &str, process: &str, ended: &mut HashMap<String, usize>) {
        for i in (0..self.steps.len()).rev() {
            let s = &self.steps[i];
            if s.path != path || s.process != process {
                continue;
            }
            if !s.subject.is_empty() && s.outcome != OUTCOME_RUNNING {
                return; // a scoped step runs once per subject: an earlier subject's run is not run again
            }
            if s.outcome == OUTCOME_RUNNING {
                self.steps.remove(i);
                for j in ended.values_mut() {
                    if *j > i {
                        *j -= 1;
                    }
                }
            }
            return;
        }
    }

    /// current sets current and stale_by on every step run (ADR 0109 §5): a success stands while (1) nothing it
    /// started from moved after it, but by a step relying on it, and (2) every step it relies on has a success
    /// standing, older than its own.
    fn current(
        &mut self,
        parent: &HashMap<String, String>,
        proc_of: &HashMap<String, String>,
        born: &HashMap<String, i64>,
        writes: &[WriteOf],
        relies: Option<Relies<'_>>,
    ) {
        // the step run a process belongs to: the step whose sub-agents it descends from
        let mut owner: HashMap<&str, usize> = HashMap::new();
        for (i, s) in self.steps.iter().enumerate() {
            for c in &s.children {
                owner.insert(c.as_str(), i);
            }
        }
        let owner: HashMap<String, usize> =
            owner.into_iter().map(|(k, v)| (k.to_string(), v)).collect();
        let step_of = |steps: &[StepRun], execution: &str| -> Option<usize> {
            let mut p = proc_of
                .get(execution)
                .map(String::as_str)
                .unwrap_or_default();
            while !p.is_empty() {
                if let Some(&i) = owner.get(p) {
                    return Some(i);
                }
                p = parent.get(p).map(String::as_str).unwrap_or_default();
            }
            steps.iter().position(|s| s.execution == execution)
        };
        let rel = |later: &StepRun, earlier: &StepRun| -> bool {
            relies.is_some_and(|f| {
                later.methodology == earlier.methodology
                    && f(&later.methodology, &later.path, &earlier.path)
            })
        };
        // path -> index of its latest run, in log order (sorted by path: the order the stale steps are named in)
        let mut latest: BTreeMap<String, usize> = BTreeMap::new();
        for i in 0..self.steps.len() {
            latest.insert(self.steps[i].path.clone(), i);
            if self.steps[i].outcome != OUTCOME_SUCCEEDED {
                continue;
            }
            self.steps[i].current = true;
            // (1) a write after it, on what existed before it ended, by nothing relying on it
            let s = &self.steps[i];
            let mut stale = None;
            for w in writes {
                if w.seq <= s.seq
                    || born.get(&w.subject).copied().unwrap_or_default() >= s.seq
                    || (!s.subject_ref.is_empty() && w.subject != s.subject_ref)
                {
                    continue;
                }
                if let Some(j) = step_of(&self.steps, &w.execution)
                    && (j == i || rel(&self.steps[j], s))
                {
                    continue;
                }
                stale = Some(format!("log entry {}", w.id));
                break;
            }
            if let Some(by) = stale {
                self.steps[i].current = false;
                self.steps[i].stale_by = by;
            }
        }
        // (2) in log order: what it relies on stands and is older; a later run of a step it relies on makes it stale
        for i in 0..self.steps.len() {
            if !self.steps[i].current {
                continue;
            }
            let mut stale = None;
            for (path, &j) in &latest {
                let (s, p) = (&self.steps[i], &self.steps[j]);
                if *path == s.path || !rel(s, p) {
                    continue;
                }
                if p.seq > s.seq
                    || (p.outcome == OUTCOME_SUCCEEDED && !p.current)
                    || p.outcome == OUTCOME_FAILED
                {
                    stale = Some(format!("step {path}"));
                    break;
                }
            }
            if let Some(by) = stale {
                self.steps[i].current = false;
                self.steps[i].stale_by = by;
            }
        }
        // a stale step makes the steps relying on it stale: repeat until nothing moves (the chain is finite)
        let mut changed = true;
        while changed {
            changed = false;
            for i in 0..self.steps.len() {
                if !self.steps[i].current {
                    continue;
                }
                let mut stale = None;
                for (path, &j) in &latest {
                    let (s, p) = (&self.steps[i], &self.steps[j]);
                    if *path != s.path && rel(s, p) && !p.current {
                        stale = Some(format!("step {path}"));
                        break;
                    }
                }
                if let Some(by) = stale {
                    self.steps[i].current = false;
                    self.steps[i].stale_by = by;
                    changed = true;
                }
            }
        }
    }
}

fn string_list(v: Option<&Value>) -> Vec<String> {
    v.and_then(Value::as_array)
        .map(|l| {
            l.iter()
                .filter_map(|x| x.as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default()
}

/// The steps of a methodology each step relies on: step path -> the paths it relies on.
pub type StepDeps = BTreeMap<String, BTreeMap<String, bool>>;

/// StepDependencies gives the steps of a methodology each step relies on (methodology Compiled::step_dependencies),
/// None when the methodology is not known. The methodology is a name or a reference at a version
/// (domain::methodology_ref).
#[async_trait::async_trait]
pub trait StepDependencies: Send + Sync {
    async fn step_dependencies(&self, ctx: &Ctx, methodology: &str) -> Option<StepDeps>;
}

/// FoldWith is the fold of the history of a change whose steps rely on one another as deps says (None: no step relies
/// on another), each methodology asked once per fold: what the graph plugs as the history facet of the blackboard
/// (graph LogFacet with HISTORY_TYPES) and the engine runs over the log it reads.
pub fn fold_with(deps: Option<Arc<dyn StepDependencies>>) -> FoldWith {
    FoldWith { deps }
}

/// The fold of [`fold_with`].
#[derive(Clone, Default)]
pub struct FoldWith {
    deps: Option<Arc<dyn StepDependencies>>,
}

impl FoldWith {
    /// The history of the change whose log entries are given. The methodologies of the step runs are asked first
    /// (the version the change follows for its own, ADR 0113), then the log is folded.
    pub async fn fold(
        &self,
        ctx: &Ctx,
        c: &Change,
        entries: &[LogEntry],
    ) -> Result<History, Error> {
        let Some(deps) = &self.deps else {
            return fold(entries, None);
        };
        let first = fold(entries, None)?;
        let mut known: HashMap<String, StepDeps> = HashMap::new();
        for s in &first.steps {
            if known.contains_key(&s.methodology) {
                continue;
            }
            let ask = if s.methodology == c.methodology {
                c.methodology_ref() // the version the change follows (ADR 0113)
            } else {
                s.methodology.clone()
            };
            let d = deps.step_dependencies(ctx, &ask).await.unwrap_or_default();
            known.insert(s.methodology.clone(), d);
        }
        let relies = |methodology: &str, later: &str, earlier: &str| -> bool {
            known
                .get(methodology)
                .and_then(|d| d.get(later))
                .and_then(|d| d.get(earlier))
                .copied()
                .unwrap_or(false)
        };
        fold(entries, Some(&relies))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::journal::entry;
    use goap_domain::{IMPACT_CREATED, ImpactEvent, impact_entry};
    use serde_json::json;

    /// logOf builds a log from records and impact events, numbering the entries in order.
    #[derive(Default)]
    struct LogOf {
        entries: Vec<LogEntry>,
    }

    impl LogOf {
        fn add(&mut self, mut e: LogEntry) {
            e.seq = self.entries.len() as i64 + 1;
            self.entries.push(e);
        }

        /// ended records a step run that ended: its step.ended record then its action record.
        fn ended(&mut self, path: &str, exec: &str, failed: bool) {
            let data = match json!({"execution": exec}) {
                Value::Object(m) => m,
                _ => unreachable!(),
            };
            self.add(
                entry(&Record {
                    id: format!("{exec}/ended"),
                    kind: KIND_STEP_ENDED.into(),
                    process_id: "p".into(),
                    methodology: "m".into(),
                    activity_ref: path.into(),
                    action: path.into(),
                    data,
                    ..Default::default()
                })
                .unwrap(),
            );
            self.add(
                entry(&Record {
                    id: exec.into(),
                    kind: KIND_ACTION.into(),
                    process_id: "p".into(),
                    methodology: "m".into(),
                    activity_ref: path.into(),
                    action: path.into(),
                    effects_met: Some(!failed),
                    ..Default::default()
                })
                .unwrap(),
            );
        }

        fn impact(&mut self, id: &str, op: ImpactOp, exec: &str) {
            self.add(
                impact_entry(&ImpactEvent {
                    id: format!("{id}{op}{exec}"),
                    impact: id.into(),
                    op,
                    execution: exec.into(),
                    ..Default::default()
                })
                .unwrap(),
            );
        }
    }

    /// analyse relies on collect, report on analyse.
    fn chain(_: &str, later: &str, earlier: &str) -> bool {
        let deps: &[(&str, &[&str])] = &[
            ("analyse", &["collect"]),
            ("report", &["analyse", "collect"]),
        ];
        deps.iter()
            .any(|(l, ds)| *l == later && ds.contains(&earlier))
    }

    fn fold_last(l: &LogOf) -> BTreeMap<String, StepRun> {
        fold(&l.entries, Some(&chain)).unwrap().last()
    }

    #[test]
    fn history_outcomes() {
        let mut l = LogOf::default();
        l.impact("I1", IMPACT_CREATED, "x0");
        l.ended("collect", "x1", false);
        l.ended("analyse", "x2", true);
        let last = fold_last(&l);
        let c = &last["collect"];
        assert!(
            c.outcome == OUTCOME_SUCCEEDED && c.current && c.runs == 1,
            "collect {c:?}"
        );
        let a = &last["analyse"];
        assert!(
            a.outcome == OUTCOME_FAILED && !a.current,
            "a failed run never stands: {a:?}"
        );
    }

    /// A rebase moving what a step started from makes it stale, and the steps relying on it; running it again does
    /// not bring back the steps relying on it, whose success is now older (ADR 0109 §5).
    #[test]
    fn history_invalidates_in_cascade() {
        let mut l = LogOf::default();
        l.impact("I1", IMPACT_CREATED, "x0");
        l.ended("collect", "x1", false);
        l.ended("analyse", "x2", false);
        l.ended("report", "x3", false);
        let last = fold_last(&l);
        assert!(
            last["collect"].current && last["analyse"].current && last["report"].current,
            "all stand: {last:?}"
        );
        l.impact("I1", IMPACT_REBASED, "");
        let last = fold_last(&l);
        for p in ["collect", "analyse", "report"] {
            assert!(
                !last[p].current,
                "{p} stands after the rebase: {:?}",
                last[p]
            );
        }
        l.ended("collect", "x4", false);
        let last = fold_last(&l);
        assert!(
            last["collect"].current
                && last["collect"].runs == 2
                && !last["analyse"].current
                && !last["report"].current,
            "collect stands again, analyse and report are to redo: {last:?}"
        );
        l.ended("analyse", "x5", false);
        l.ended("report", "x6", false);
        let last = fold_last(&l);
        assert!(
            last["analyse"].current && last["report"].current,
            "all stand again: {last:?}"
        );
    }

    /// What a step relying on another writes never makes the other stale; what any other writes does.
    #[test]
    fn history_writes_of_successors_do_not_count() {
        let mut l = LogOf::default();
        l.impact("I1", IMPACT_CREATED, "x0");
        l.ended("collect", "x1", false);
        l.ended("analyse", "x2", false);
        l.impact("I1", IMPACT_UPDATED, "x2"); // analyse edits what collect read, after collect
        let last = fold_last(&l);
        assert!(
            last["collect"].current,
            "collect stands: {:?}",
            last["collect"]
        );
        l.impact("I2", IMPACT_CREATED, "");
        l.impact("I2", IMPACT_UPDATED, ""); // a new impact: nothing a step saw
        let last = fold_last(&l);
        assert!(
            last["collect"].current && last["analyse"].current,
            "a new impact moves nothing: {last:?}"
        );
        l.impact("I1", IMPACT_UPDATED, ""); // a person's edit
        let last = fold_last(&l);
        assert!(
            !last["collect"].current && !last["analyse"].current,
            "an edit from outside makes them stale: {last:?}"
        );
    }

    struct Chain;

    #[async_trait::async_trait]
    impl StepDependencies for Chain {
        async fn step_dependencies(&self, _: &Ctx, methodology: &str) -> Option<StepDeps> {
            assert_eq!(methodology, "m@1.0.0", "the version the change follows");
            let mut d = StepDeps::new();
            d.entry("analyse".into())
                .or_default()
                .insert("collect".into(), true);
            Some(d)
        }
    }

    #[tokio::test]
    async fn fold_with_asks_the_version_of_the_change() {
        let mut l = LogOf::default();
        l.impact("I1", IMPACT_CREATED, "x0");
        l.ended("collect", "x1", false);
        l.ended("analyse", "x2", false);
        l.impact("I1", IMPACT_UPDATED, "x2");
        let c = Change {
            methodology: "m".into(),
            methodology_version: "1.0.0".into(),
            ..Default::default()
        };
        let h = fold_with(Some(Arc::new(Chain)))
            .fold(&Ctx::background(), &c, &l.entries)
            .await
            .unwrap();
        assert!(h.last()["collect"].current, "analyse relies on collect");
        let h = fold_with(None)
            .fold(&Ctx::background(), &c, &l.entries)
            .await
            .unwrap();
        assert!(!h.last()["collect"].current, "no step relies on another");
        assert_eq!(h.last()["collect"].stale_by, "log entry I1updatedx2");
    }

    #[test]
    fn history_types_name_the_streams() {
        assert_eq!(
            HISTORY_TYPES[0],
            format!("{STREAM_JOURNAL}.{KIND_PROCESS_STARTED}")
        );
        assert_eq!(
            HISTORY_TYPES[1],
            format!("{STREAM_JOURNAL}.{KIND_PROCESS_ENDED}")
        );
        assert_eq!(HISTORY_TYPES[2], format!("{STREAM_JOURNAL}.{KIND_ACTION}"));
        assert_eq!(
            HISTORY_TYPES[3],
            format!("{STREAM_JOURNAL}.{KIND_STEP_ENDED}")
        );
        assert_eq!(HISTORY_TYPES[4], format!("{LOG_IMPACT}."));
        assert_eq!(HISTORY_TYPES[5], format!("{LOG_FACT}."));
    }
}
