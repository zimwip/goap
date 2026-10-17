//! How the assessments of the subjects of a change read (ADR 0116, `assessment.go`).

use std::sync::Arc;

use goap_condition::{AssessmentState as ConditionAssessmentState, Result as CondResult};
use goap_condition::{StateRule, SubjectView, compile_assessment_views};
use goap_domain::Blackboard;
use serde::{Deserialize, Serialize};

use crate::compile::CompileState;
use crate::methodology::Compiled;
use crate::util::{Conds, is_false, nd, st};

/// AssessmentView says how the assessments of the subjects its pattern matches read (ADR 0116): the states they go
/// through, the first that holds wins. Subject is a subject key (description, title, intent, impact:<key>,
/// item:<key>) or a glob over them (impact:*, item:*, *).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct AssessmentView {
    #[serde(deserialize_with = "st")]
    pub subject: String,
    #[serde(deserialize_with = "nd")]
    pub states: Vec<AssessmentState>,
}

/// AssessmentState is one state of an assessment: it holds when each condition has the value given and When holds.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct AssessmentState {
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub when: String,
    #[serde(skip_serializing_if = "Conds::is_empty", deserialize_with = "nd")]
    pub conditions: Conds,
    #[serde(deserialize_with = "st")]
    pub tone: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub label: String,
    #[serde(skip_serializing_if = "is_false")]
    pub open: bool,
}

impl AssessmentView {
    fn rules(&self) -> SubjectView {
        SubjectView {
            subject: self.subject.clone(),
            states: self
                .states
                .iter()
                .map(|s| StateRule {
                    when: s.when.clone(),
                    conditions: s.conditions.clone(),
                    tone: s.tone.clone(),
                    label: s.label.clone(),
                    open: s.open,
                })
                .collect(),
        }
    }
}

impl CompileState<'_> {
    /// compileAssessments checks and compiles the assessment views (every condition is known by then).
    pub(crate) fn compile_assessments(&mut self) {
        if self.m.assessments.is_empty() {
            return;
        }
        let views: Vec<SubjectView> = self.m.assessments.iter().map(|v| v.rules()).collect();
        let known = &self.known;
        let f = |name: &str| known.contains(name);
        let (compiled, issues) = compile_assessment_views(&views, Some(&f));
        for is in issues {
            self.add(format!("assessments{}", is.path), is.message);
        }
        self.assessments = Some(Arc::new(compiled));
    }
}

impl Compiled {
    /// AssessmentStates is how the assessment of each subject of the change reads now, given the evaluation of the
    /// conditions on the same blackboard.
    pub fn assessment_states(
        &self,
        bb: &Blackboard,
        res: &CondResult,
    ) -> Vec<ConditionAssessmentState> {
        goap_condition::assessment_states(self.assessments.as_deref(), bb, res)
    }
}
