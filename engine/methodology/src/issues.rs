//! The activity an issue is about (`issues.go`).

use std::sync::LazyLock;

use regex::Regex;

use crate::methodology::Methodology;
use crate::process::Step;

static ACTIVITY_PATH_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^(processes|methods)\[(\d+)\]((?:\.steps\[\d+\])*)").unwrap());
static STEP_INDEX_RE: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"\.steps\[(\d+)\]").unwrap());

impl Methodology {
    /// ActivityOf maps the definition path of an issue ("processes[0].steps[2].steps[1].pre") to the flow path of
    /// the activity it is about ("<process>/<step>/<sub-step>"); "" for an issue that is about no process, method or
    /// step.
    pub fn activity_of(&self, path: &str) -> String {
        let Some(g) = ACTIVITY_PATH_RE.captures(path) else {
            return String::new();
        };
        let i: usize = g[2].parse().unwrap_or(usize::MAX);
        let (name, mut steps): (&str, &[Step]) = if &g[1] == "processes" {
            match self.processes.get(i) {
                Some(p) => (&p.name, &p.steps),
                None => return String::new(),
            }
        } else {
            match self.methods.get(i) {
                Some(m) => (&m.name, &m.steps),
                None => return String::new(),
            }
        };
        let mut out = name.to_string();
        for idx in STEP_INDEX_RE.captures_iter(&g[3]) {
            let j: usize = idx[1].parse().unwrap_or(usize::MAX);
            let Some(s) = steps.get(j) else {
                return out;
            };
            out.push('/');
            out.push_str(&s.name);
            steps = &s.steps;
        }
        out
    }
}
