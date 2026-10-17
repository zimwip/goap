use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

use goap_llm as llm;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::Error;

/// Case is one evaluation: what is sent, the tools the model may call (mocked: they answer what the case says) and
/// what the answer must satisfy.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Case {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    /// Model is the alias or "provider/model" (default "default"); a run may override it.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model: String,
    /// Prompt "engine" puts the system text of the engine's LLM actions (the item protocol) before System.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub prompt: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub system: String,
    pub messages: Vec<Turn>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub json: bool,
    #[serde(skip_serializing_if = "is_zero")]
    pub max_tokens: i64,
    /// MaxRounds bounds the tool rounds (default DEFAULT_MAX_ROUNDS).
    #[serde(skip_serializing_if = "is_zero")]
    pub max_rounds: i64,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<Tool>,
    pub expect: Expect,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub judge: Option<Judge>,
    /// Runs is how many times the case runs (default 1); MinPassRate the share of passing runs for the case to pass
    /// (default 1).
    #[serde(skip_serializing_if = "is_zero")]
    pub runs: i64,
    #[serde(skip_serializing_if = "is_zero_f64")]
    pub min_pass_rate: f64,
    /// File is where the case was read from.
    #[serde(skip)]
    pub file: String,
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

fn is_zero_f64(n: &f64) -> bool {
    *n == 0.0
}

/// Turn is a message of the conversation sent.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Turn {
    pub role: String,
    pub content: String,
}

/// Tool is a mocked tool: its declaration and what it answers. The first of Responses whose Args are a subset of the
/// call's arguments answers; else Result, or Error when set.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Tool {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(skip_serializing_if = "Map::is_empty")]
    pub input_schema: Map<String, Value>,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub result: Value,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub responses: Vec<Response>,
}

/// Response is the answer of a mocked tool to the calls whose arguments include Args.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Response {
    pub args: Map<String, Value>,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub result: Value,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
}

/// Expect is what a run must satisfy; every check set is one check of the run.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Expect {
    /// JSON: the answer is a JSON object.
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub json: bool,
    /// Items: the answer is {"items": [...]} and each of these is a subset of one of its items (maps compared key by
    /// key, recursively; lists element by element).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub items: Vec<Map<String, Value>>,
    /// Contains / NotContains: texts the answer holds or must not hold (case-insensitive).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub contains: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub not_contains: Vec<String>,
    /// Trajectory: the tools called, compared with Match exact (the same calls in the same order), in_order (these
    /// calls in this order, others between allowed) or any_order (these calls, in any order, others allowed).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub trajectory: Option<Trajectory>,
    /// MaxToolCalls bounds the calls (0: no bound); NoToolErrors fails a run where a call failed.
    #[serde(skip_serializing_if = "is_zero")]
    pub max_tool_calls: i64,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub no_tool_errors: bool,
}

/// Trajectory matches the tools called.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Trajectory {
    /// exact (default) | in_order | any_order
    #[serde(rename = "match", skip_serializing_if = "String::is_empty")]
    pub r#match: String,
    pub tools: Vec<String>,
}

/// Judge scores the answer with a model against a rubric (0 to 1); the run fails under Threshold (default 0.7).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Judge {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model: String,
    pub rubric: String,
    #[serde(skip_serializing_if = "is_zero_f64")]
    pub threshold: f64,
}

/// Defaults.
pub const DEFAULT_MAX_ROUNDS: i64 = 6;
pub const DEFAULT_THRESHOLD: f64 = 0.7;

/// Trajectory matches.
pub const MATCH_EXACT: &str = "exact";
pub const MATCH_IN_ORDER: &str = "in_order";
pub const MATCH_ANY_ORDER: &str = "any_order";

/// file is a case file: one case, or a list under "cases".
#[derive(Deserialize, Default)]
#[serde(default)]
struct File {
    #[serde(flatten)]
    case: Case,
    cases: Vec<Case>,
}

fn walk(dir: &Path, files: &mut Vec<PathBuf>) -> Result<(), Error> {
    for e in std::fs::read_dir(dir)? {
        let p = e?.path();
        if p.is_dir() {
            walk(&p, files)?;
        } else if p.extension().is_some_and(|x| x == "yaml" || x == "yml") {
            files.push(p);
        }
    }
    Ok(())
}

/// Load reads the cases of the files and directories given (*.yaml / *.yml, recursively), sorted by file then order.
pub fn load(paths: &[impl AsRef<Path>]) -> Result<Vec<Case>, Error> {
    let mut files = Vec::new();
    for p in paths {
        let p = p.as_ref();
        let st = std::fs::metadata(p)?;
        if !st.is_dir() {
            files.push(p.to_path_buf());
            continue;
        }
        walk(p, &mut files)?;
    }
    let mut files: Vec<String> = files.iter().map(|f| f.display().to_string()).collect();
    files.sort();
    let mut out = Vec::new();
    for f in files {
        let b = std::fs::read(&f)?;
        let mut cs = parse(&b).map_err(|e| Error::File {
            file: f.clone(),
            source: Box::new(e),
        })?;
        for c in &mut cs {
            c.file = f.clone();
        }
        out.extend(cs);
    }
    Ok(out)
}

/// Parse reads the cases of a YAML document and checks them.
pub fn parse(b: &[u8]) -> Result<Vec<Case>, Error> {
    let f: File = serde_yaml_ng::from_slice(b)?;
    let mut cases = f.cases;
    if !f.case.name.is_empty() || !f.case.messages.is_empty() {
        cases.insert(0, f.case);
    }
    if cases.is_empty() {
        return Err(Error::Invalid("no case".into()));
    }
    let mut seen = BTreeSet::new();
    for (i, c) in cases.iter().enumerate() {
        if let Err(e) = c.check() {
            return Err(Error::Invalid(format!("case {} ({}): {e}", i + 1, c.name)));
        }
        if !seen.insert(c.name.clone()) {
            return Err(Error::Invalid(format!("case {:?} twice", c.name)));
        }
    }
    Ok(cases)
}

impl Case {
    fn check(&self) -> Result<(), String> {
        if self.name.is_empty() {
            return Err("a case has a name".into());
        }
        if self.messages.first().is_none_or(|m| m.role != "user") {
            return Err("the messages start with a user message".into());
        }
        if !self.prompt.is_empty() && self.prompt != "engine" {
            return Err("prompt is \"engine\" or nothing".into());
        }
        if self.min_pass_rate < 0.0 || self.min_pass_rate > 1.0 {
            return Err("minPassRate is between 0 and 1".into());
        }
        if self
            .judge
            .as_ref()
            .is_some_and(|j| j.rubric.trim().is_empty())
        {
            return Err("a judge has a rubric".into());
        }
        if let Some(t) = &self.expect.trajectory
            && !t.r#match.is_empty()
            && ![MATCH_EXACT, MATCH_IN_ORDER, MATCH_ANY_ORDER].contains(&t.r#match.as_str())
        {
            return Err(format!(
                "trajectory match {:?} (exact, in_order or any_order)",
                t.r#match
            ));
        }
        let mut names = BTreeSet::new();
        for t in &self.tools {
            if t.name.is_empty() || !names.insert(t.name.as_str()) {
                return Err("tool names are set and unique".into());
            }
        }
        for m in &self.messages {
            if m.role != "user" && m.role != "assistant" {
                return Err(format!("message role {:?} (user or assistant)", m.role));
            }
        }
        Ok(())
    }

    pub(crate) fn llm_messages(&self) -> Vec<llm::Message> {
        self.messages
            .iter()
            .map(|m| llm::Message {
                role: m.role.clone(),
                content: m.content.clone(),
                ..Default::default()
            })
            .collect()
    }
}
