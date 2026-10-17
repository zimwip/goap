//! How the assessment of a subject reads (ADR 0116): a methodology declares, per subject, the states its assessment
//! goes through, each bound to conditions or a CEL expression, with the tone it is shown in, a label and whether the
//! bubble opens by itself. The server evaluates them against the change; the web only renders the result.
use std::{
    collections::BTreeMap,
    sync::{Arc, LazyLock},
};

use cel::Program;
use goap_assess::{IMPACT_PREFIX, ITEM_PREFIX, SUBJECT_DESCRIPTION, SUBJECT_INTENT, SUBJECT_TITLE};
use goap_domain::Blackboard;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    activation::{Activation, Object, Overlay},
    check::Ty,
    condition::{Error, Result, compile_checked, condition_decls, eval_bool, new_env},
    library::ACCEPTABLE_SCORE,
    pathmatch::path_match,
    scope::{SCOPE_ACTION, SCOPE_IMPACT, SCOPE_ITEM, SCOPE_RISK, subject_id, subjects_of},
    unparse::go_quote,
};

/// Tones of an assessment state.
pub const TONE_OK: &str = "ok";
pub const TONE_WARN: &str = "warn";
pub const TONE_DANGER: &str = "danger";
pub const TONE_INFO: &str = "info";
pub const TONE_NEUTRAL: &str = "neutral";

/// Tones lists the tones, in a stable order.
pub const TONES: [&str; 5] = [TONE_OK, TONE_WARN, TONE_DANGER, TONE_INFO, TONE_NEUTRAL];

/// StateRule is one state of the view of an assessment: it holds when every condition has the value given (a scoped
/// condition read on the subject) and its when expression is true; neither: it always holds.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct StateRule {
    pub when: String,
    pub conditions: BTreeMap<String, bool>,
    pub tone: String,
    pub label: String,
    pub open: bool,
}

/// SubjectView is the view of the assessments of the subjects its pattern matches (a subject key, or a glob:
/// "impact:*", "*"): its states, the first that holds wins.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SubjectView {
    pub subject: String,
    pub states: Vec<StateRule>,
}

/// AssessmentState is how the assessment of one subject reads now.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct AssessmentState {
    pub subject: String,
    pub score: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub verdict: String,
    pub current: bool,
    pub answered: bool,
    pub tone: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub open: bool,
    /// Errors are the states that could not be evaluated (skipped).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub errors: Vec<String>,
}

fn rule(when: &str, tone: &str, label: &str) -> StateRule {
    StateRule {
        when: when.into(),
        tone: tone.into(),
        label: label.into(),
        ..Default::default()
    }
}

/// DefaultAssessmentViews are the views of the subjects no view of the methodology settles: an impact is processed
/// once its review is decided; any other subject is accepted once it is rated as it reads, its last remark answered,
/// with a score of ACCEPTABLE_SCORE or more.
pub static DEFAULT_ASSESSMENT_VIEWS: LazyLock<Vec<SubjectView>> = LazyLock::new(|| {
    vec![
        SubjectView {
            subject: format!("{IMPACT_PREFIX}*"),
            states: vec![
                rule(
                    r#"subject.entry != null && subject.entry.review in ["accepted", "rejected"]"#,
                    TONE_OK,
                    "processed",
                ),
                rule("", TONE_WARN, "not processed yet"),
            ],
        },
        SubjectView {
            subject: "*".into(),
            states: vec![
                rule("!assessment.current", TONE_WARN, "changed since its rating"),
                rule(
                    "!assessment.answered",
                    TONE_WARN,
                    "your remark awaits an answer",
                ),
                rule(
                    &format!("assessment.score >= {ACCEPTABLE_SCORE}"),
                    TONE_OK,
                    "accepted",
                ),
                rule("", TONE_WARN, "to be improved"),
            ],
        },
    ]
});

/// AssessmentViews are compiled views.
#[derive(Debug, Clone, Default)]
pub struct AssessmentViews {
    views: Vec<CompiledView>,
}

#[derive(Debug, Clone)]
struct CompiledView {
    subject: String,
    states: Vec<CompiledRule>,
}

#[derive(Debug, Clone)]
struct CompiledRule {
    rule: StateRule,
    prog: Option<Arc<Program>>,
}

/// The declared variables of the when of a state (Go's `AssessmentEnv`): the ones of the conditions plus
/// `assessment` (the rating, as an entry of assessments), `subject` ({key, kind, id, entry: the entry of the change,
/// of changeImpacts or of items it is about, null when there is none; conditions: the scoped conditions on it}) and
/// `conditions` (the conditions of the change).
pub fn assessment_variables() -> Vec<&'static str> {
    assessment_decls().into_iter().map(|(n, _)| n).collect()
}

fn assessment_decls() -> Vec<(&'static str, Ty)> {
    let mut d = condition_decls();
    d.push(("assessment", Ty::map()));
    d.push(("subject", Ty::map()));
    d.push(("conditions", Ty::map_of(Ty::Bool)));
    d
}

/// CheckSubjectPattern checks the subject pattern of a view.
pub fn check_subject_pattern(p: &str) -> std::result::Result<(), Error> {
    if p.trim().is_empty() {
        return Err(Error::Compile(
            "subject required: a subject key (description, title, intent, impact:<key>, item:<key>) or a glob (impact:*, *)"
                .into(),
        ));
    }
    path_match(p, "")
        .map(|_| ())
        .map_err(|e| Error::Compile(e.to_string()))
}

/// ViewIssue is an issue of a view, at a path relative to the list of views.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ViewIssue {
    pub path: String,
    pub message: String,
}

fn compile_bool(expr: &str) -> std::result::Result<Program, Error> {
    let (prog, t) = compile_checked(&new_env(), &assessment_decls(), expr)?;
    if t != Ty::Bool && t != Ty::Dyn {
        return Err(Error::Compile(format!("must return bool, got {t}")));
    }
    Ok(prog)
}

/// CompileAssessmentViews compiles views; known says whether a condition exists (None: every one). The issues are
/// given per view and state ("[i].states[j].when"), the views compiled without the states that have one.
pub fn compile_assessment_views(
    views: &[SubjectView],
    known: Option<&dyn Fn(&str) -> bool>,
) -> (AssessmentViews, Vec<ViewIssue>) {
    let mut issues = Vec::new();
    let mut out = AssessmentViews::default();
    for (i, v) in views.iter().enumerate() {
        let at = format!("[{i}]");
        if let Err(e) = check_subject_pattern(&v.subject) {
            issues.push(ViewIssue {
                path: format!("{at}.subject"),
                message: e.to_string(),
            });
            continue;
        }
        if v.states.is_empty() {
            issues.push(ViewIssue {
                path: format!("{at}.states"),
                message: "at least one state required".into(),
            });
        }
        let mut cv = CompiledView {
            subject: v.subject.clone(),
            states: Vec::new(),
        };
        for (j, s) in v.states.iter().enumerate() {
            let sat = format!("{at}.states[{j}]");
            let mut ok = true;
            if !TONES.contains(&s.tone.as_str()) {
                issues.push(ViewIssue {
                    path: format!("{sat}.tone"),
                    message: format!(
                        "unknown tone {} (one of {})",
                        go_quote(&s.tone),
                        TONES.join(", ")
                    ),
                });
                ok = false;
            }
            for k in s.conditions.keys() {
                if let Some(known) = known
                    && !known(k)
                {
                    issues.push(ViewIssue {
                        path: format!("{sat}.conditions.{k}"),
                        message: format!("unknown condition {}", go_quote(k)),
                    });
                    ok = false;
                }
            }
            let mut r = CompiledRule {
                rule: s.clone(),
                prog: None,
            };
            if !s.when.is_empty() {
                match compile_bool(&s.when) {
                    Ok(p) => r.prog = Some(Arc::new(p)),
                    Err(e) => {
                        issues.push(ViewIssue {
                            path: format!("{sat}.when"),
                            message: e.to_string(),
                        });
                        ok = false;
                    }
                }
            }
            if ok {
                cv.states.push(r);
            }
        }
        out.views.push(cv);
    }
    (out, issues)
}

static DEFAULT_VIEWS: LazyLock<AssessmentViews> = LazyLock::new(|| {
    let (v, issues) = compile_assessment_views(&DEFAULT_ASSESSMENT_VIEWS, None);
    if !issues.is_empty() {
        panic!("default assessment views: {issues:?}");
    }
    v
});

impl AssessmentViews {
    /// States evaluates the views against the blackboard and the evaluation of its conditions (see
    /// [`assessment_states`]).
    pub fn states(&self, bb: &Blackboard, res: &Result) -> Vec<AssessmentState> {
        assessment_states(Some(self), bb, res)
    }
}

/// States evaluates the views (None: the defaults only) against the blackboard and the evaluation of its
/// conditions: one state per assessed subject, in the order of the assessments. The first view whose pattern matches
/// the subject and one of whose states holds decides, then the defaults.
pub fn assessment_states(
    v: Option<&AssessmentViews>,
    bb: &Blackboard,
    res: &Result,
) -> Vec<AssessmentState> {
    let as_ = goap_assess::assessments(bb);
    let act = Activation::new(bb);
    let records = act
        .get("assessments")
        .and_then(|x| x.as_array())
        .cloned()
        .unwrap_or_default();
    let conds = res.state.clone();
    let conds_v = Value::Object(
        conds
            .iter()
            .map(|(k, b)| (k.clone(), Value::Bool(*b)))
            .collect(),
    );
    let mut out = Vec::with_capacity(as_.len());
    for (i, a) in as_.iter().enumerate() {
        let mut st = AssessmentState {
            subject: a.key.clone(),
            score: a.score,
            verdict: a.verdict.clone(),
            current: a.current,
            answered: a.answered,
            tone: TONE_NEUTRAL.into(),
            ..Default::default()
        };
        let (sub, scoped) = subject_of(&act, res, &a.key);
        let sact = Overlay::new(&act)
            .with("assessment", records.get(i).unwrap_or(&Value::Null))
            .with("subject", &Value::Object(sub))
            .with("conditions", &conds_v);
        let mut decided = false;
        for set in [v, Some(&*DEFAULT_VIEWS)] {
            let Some(set) = set else { continue };
            if decided {
                continue;
            }
            for cv in &set.views {
                if !path_match(&cv.subject, &a.key).unwrap_or(false) {
                    continue;
                }
                if let Some(r) = cv.decide(&sact, &conds, &scoped, &mut st) {
                    st.tone = r.tone.clone();
                    st.label = r.label.clone();
                    st.open = r.open;
                    decided = true;
                    break;
                }
            }
        }
        out.push(st);
    }
    out
}

impl CompiledView {
    /// decide returns the first state of the view that holds on the subject.
    fn decide(
        &self,
        act: &Overlay<'_, '_>,
        conds: &BTreeMap<String, bool>,
        scoped: &BTreeMap<String, bool>,
        st: &mut AssessmentState,
    ) -> Option<&StateRule> {
        for (j, r) in self.states.iter().enumerate() {
            let mut holds = r
                .rule
                .conditions
                .iter()
                .all(|(k, want)| scoped.get(k).or_else(|| conds.get(k)) == Some(want));
            if holds && let Some(p) = &r.prog {
                match eval_bool(p, act) {
                    Ok(b) => holds = b,
                    Err(e) => {
                        st.errors
                            .push(format!("{} states[{}]: {}", self.subject, j, e));
                        continue;
                    }
                }
            }
            if holds {
                return Some(&r.rule);
            }
        }
        None
    }
}

/// subjectOf is the subject of an assessment as the when expressions see it, and the values of the scoped conditions
/// about it.
fn subject_of(act: &Activation<'_>, res: &Result, key: &str) -> (Object, BTreeMap<String, bool>) {
    let (kind, id) = match key.split_once(':') {
        Some((k, i)) => (k, i),
        None => (key, ""),
    };
    let mut entry = Value::Null;
    let mut refs: Vec<(&str, String)> = Vec::new();
    if key == SUBJECT_DESCRIPTION || key == SUBJECT_TITLE || key == SUBJECT_INTENT {
        entry = act.get("change").cloned().unwrap_or(Value::Null);
    } else if format!("{kind}:") == IMPACT_PREFIX {
        for e in subjects_of(act, SCOPE_IMPACT) {
            if subject_id(SCOPE_IMPACT, &e) == id {
                entry = Value::Object(e);
            }
        }
        refs.push((SCOPE_IMPACT, id.to_string()));
    } else if format!("{kind}:") == ITEM_PREFIX {
        for e in subjects_of(act, SCOPE_ITEM) {
            let k = e
                .get("data")
                .and_then(|d| d.get("key"))
                .and_then(|k| k.as_str())
                .unwrap_or("");
            if e.get("id").and_then(|x| x.as_str()) == Some(id) || (!k.is_empty() && k == id) {
                refs.push((SCOPE_ITEM, subject_id(SCOPE_ITEM, &e)));
                entry = Value::Object(e);
            }
        }
        refs.push((SCOPE_RISK, id.to_string()));
        refs.push((SCOPE_ACTION, id.to_string()));
    }
    let mut scoped = BTreeMap::new();
    for (scope, rid) in &refs {
        for s in res.subjects.get(*scope).into_iter().flatten() {
            if &s.id == rid {
                scoped.extend(s.values.iter().map(|(k, v)| (k.clone(), *v)));
            }
        }
    }
    let values: Map<String, Value> = scoped
        .iter()
        .map(|(k, b)| (k.clone(), Value::Bool(*b)))
        .collect();
    let id = if kind == key { "" } else { id };
    let mut m = Map::new();
    m.insert("key".into(), Value::from(key));
    m.insert("kind".into(), Value::from(kind));
    m.insert("id".into(), Value::from(id));
    m.insert("entry".into(), entry);
    m.insert("conditions".into(), Value::Object(values));
    (m, scoped)
}
