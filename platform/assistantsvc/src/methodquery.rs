//! The query engine of methodology_query (ADR 0094, `methodquery.go`): a pure function over the declared definition of
//! a methodology. It reads nothing, calls nothing and never builds more than the caller asked for; the registry access
//! is the Methodologies port of the Service. Every result is small by construction: filtered, paginated, projected,
//! and capped in bytes.

use std::collections::{BTreeMap, HashMap};

use goap_methodology::{Methodology, Step};
use serde::Serialize;
use serde_json::{Map, Value, json};

use crate::util::{clip, go_json, q};

/// Kinds of element a methodology holds.
pub const KIND_AGENT: &str = "agent";
pub const KIND_ACTION: &str = "action";
pub const KIND_GOAL: &str = "goal";
pub const KIND_PROCESS: &str = "process";
pub const KIND_STEP: &str = "step";
pub const KIND_ROLE: &str = "role";
pub const KIND_METHOD: &str = "method";
pub const KIND_TRIGGER: &str = "trigger";
pub const KIND_CONDITION: &str = "condition";
pub const KIND_LIBRARY: &str = "library";

/// ElementKinds lists the kinds, in the order of the overview.
pub const ELEMENT_KINDS: [&str; 10] = [
    KIND_AGENT,
    KIND_ACTION,
    KIND_GOAL,
    KIND_PROCESS,
    KIND_STEP,
    KIND_ROLE,
    KIND_METHOD,
    KIND_TRIGGER,
    KIND_CONDITION,
    KIND_LIBRARY,
];

/// Details of an item.
pub const DETAIL_NAMES: &str = "names";
pub const DETAIL_SUMMARY: &str = "summary";
pub const DETAIL_FULL: &str = "full";

/// Limits of a query.
pub const DEFAULT_QUERY_LIMIT: i64 = 20;
pub const MAX_QUERY_LIMIT: i64 = 50;
/// MaxFullItems caps the items of a result given in full detail.
pub const MAX_FULL_ITEMS: i64 = 3;
/// MaxQueryBytes caps the JSON of a whole result.
pub const MAX_QUERY_BYTES: usize = 6 << 10;

/// a description in a summary: first line, cut
pub(crate) const SUMMARY_TEXT: usize = 160;
/// a string of a full definition
const FULL_TEXT: usize = 2000;
/// the items of a list in a summary
const SUMMARY_LIST: usize = 8;

/// Query is the request of methodology_query, validated by run_query.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Query {
    pub kind: String,
    /// exact name or glob (* and ?), case-insensitive
    pub name: String,
    /// case-insensitive text in name, description and examples
    pub q: String,
    /// only the children of this element
    pub parent: String,
    pub fields: Vec<String>,
    pub detail: String,
    pub limit: i64,
    pub offset: i64,
}

/// QueryResult is the answer.
#[derive(Debug, Clone, Default, Serialize)]
pub struct QueryResult {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub kind: String,
    pub total: i64,
    pub returned: i64,
    pub offset: i64,
    pub truncated: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next: Option<NextPage>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub counts: BTreeMap<String, i64>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub hint: String,
    pub items: Vec<Map<String, Value>>,
}

/// NextPage is where the next page of the same query starts.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize)]
pub struct NextPage {
    pub offset: i64,
}

/// element is one thing of a methodology, as the query sees it.
#[derive(Debug, Clone)]
struct Element {
    kind: &'static str,
    name: String,
    /// the last segment of a step path, for the name filter
    short: String,
    /// the elements it belongs to: the process or step of a step, the agents of an action...
    parents: Vec<String>,
    /// lower-cased name, description and examples
    search: String,
    desc: String,
    full: Map<String, Value>,
    /// the fields of the summary
    summary: &'static [&'static str],
}

/// actionKind avoids the clash of an action's own kind with the kind of the element.
const FIELD_ACTION_KIND: &str = "actionKind";
const FIELD_PARENTS: &str = "parents";
const FIELD_HOW: &str = "how";

/// elementFields is the valid fields of each kind, in the order of the definition (the json fields of its Go struct,
/// after kind and parents; an action's kind is actionKind; a step adds how).
fn element_fields(kind: &str) -> &'static [&'static str] {
    match kind {
        KIND_AGENT => &[
            "kind",
            "parents",
            "name",
            "description",
            "examples",
            "planner",
            "model",
            "actions",
            "goals",
            "triggers",
            "mcps",
            "roles",
            "role",
            "persistent",
        ],
        KIND_ACTION => &[
            "kind",
            "parents",
            "name",
            "description",
            "actionKind",
            "pre",
            "effects",
            "cost",
            "expects",
            "verify",
            "permission",
            "roles",
            "model",
            "prompt",
            "loop",
            "tool",
            "mcps",
            "builtin",
            "instructions",
            "params",
            "nodeTypes",
            "form",
            "language",
            "code",
            "utility",
            "specializes",
            "when",
            "priority",
            "incremental",
            "on",
            "emits",
            "scope",
            "where",
            "parallel",
        ],
        KIND_GOAL => &[
            "kind",
            "parents",
            "name",
            "description",
            "examples",
            "pre",
            "value",
        ],
        KIND_PROCESS => &[
            "kind",
            "parents",
            "name",
            "description",
            "examples",
            "references",
            "steps",
            "on",
        ],
        KIND_STEP => &[
            "kind",
            "parents",
            "name",
            "description",
            "instructions",
            "references",
            "guidance",
            "checklist",
            "deliverables",
            "roles",
            "pre",
            "done",
            "steps",
            "action",
            "actions",
            "process",
            "method",
            "foreach",
            "groupBy",
            "where",
            "on",
            "review",
            "how",
        ],
        KIND_ROLE => &["kind", "parents", "name", "description", "operations"],
        KIND_METHOD => &[
            "kind",
            "parents",
            "name",
            "for",
            "when",
            "priority",
            "description",
            "guidance",
            "checklist",
            "deliverables",
            "references",
            "roles",
            "steps",
            "actions",
            "done",
            "planner",
            "model",
            "mcps",
            "on",
            "persistent",
        ],
        KIND_TRIGGER => &[
            "kind",
            "parents",
            "name",
            "description",
            "type",
            "event",
            "filter",
            "schedule",
            "goal",
            "intent",
            "target",
            "supersede",
            "roles",
            "enabled",
        ],
        KIND_CONDITION => &[
            "kind",
            "parents",
            "name",
            "description",
            "expr",
            "scope",
            "where",
            "all",
            "any",
            "none",
            "empty",
        ],
        KIND_LIBRARY => &["name", "kind", "parents", "conditions"],
        _ => &[],
    }
}

/// summaryFields are the few defining fields of each kind (after name, kind and description).
fn summary_fields(kind: &str) -> &'static [&'static str] {
    match kind {
        KIND_AGENT => &["planner", "roles", "goals", "actions"],
        KIND_ACTION => &[FIELD_ACTION_KIND, "roles", "pre", "effects"],
        KIND_GOAL => &["pre", "value"],
        KIND_PROCESS => &["steps"],
        KIND_STEP => &[FIELD_HOW, "action", "process", "roles", "steps"],
        KIND_METHOD => &["for", "when", "priority"],
        KIND_TRIGGER => &["type", "event", "schedule", "enabled"],
        KIND_CONDITION => &["expr"],
        KIND_LIBRARY => &["conditions"],
        _ => &[],
    }
}

/// validFields returns the valid fields of a kind, or of every kind when it is empty.
fn valid_fields(kind: &str) -> Vec<&'static str> {
    if !kind.is_empty() {
        return element_fields(kind).to_vec();
    }
    let mut out: Vec<&'static str> = Vec::new();
    for k in ELEMENT_KINDS {
        for f in element_fields(k) {
            if !out.contains(f) {
                out.push(f);
            }
        }
    }
    out
}

/// RunQuery answers q over the methodologies given by name; with one it adds the counts by kind when no kind is asked.
pub fn run_query(q: Query, ms: &[&Methodology]) -> Result<QueryResult, String> {
    let q = q.validated()?;
    let multi = ms.len() > 1;
    let mut matches: Vec<(&str, Element)> = Vec::new();
    let mut counts: BTreeMap<String, i64> = BTreeMap::new();
    for m in ms {
        for e in elements_of(m) {
            if q.matches(&e) {
                *counts.entry(e.kind.to_string()).or_default() += 1;
                matches.push((m.name.as_str(), e));
            }
        }
    }
    let mut res = QueryResult {
        kind: q.kind.clone(),
        total: matches.len() as i64,
        offset: q.offset,
        ..Default::default()
    };
    if ms.len() == 1 {
        res.methodology = ms[0].name.clone();
    }
    if q.kind.is_empty() && !multi {
        res.counts = counts;
    }
    let (mut limit, mut full_cut) = (q.limit, false);
    if q.detail == DETAIL_FULL && q.fields.is_empty() && limit > MAX_FULL_ITEMS {
        limit = MAX_FULL_ITEMS;
        full_cut = true;
    }
    let end = (matches.len() as i64).min(q.offset + limit);
    let mut i = q.offset;
    while i < end {
        let (m, e) = &matches[i as usize];
        let mut item = e.render(&q);
        if multi {
            item.insert("methodology".into(), json!(m));
        }
        res.items.push(item);
        if go_json(&res).len() > MAX_QUERY_BYTES {
            res.items.pop();
            res.truncated = true;
            break;
        }
        i += 1;
    }
    res.returned = res.items.len() as i64;
    if q.offset + res.returned < res.total {
        res.next = Some(NextPage {
            offset: q.offset + res.returned,
        });
    }
    if res.truncated && res.returned == 0 {
        res.hint = "the first item alone exceeds the size cap: narrow with kind, name, q or parent, and project with fields".into();
    } else if res.truncated {
        res.hint = "result cut at the size cap: narrow with kind, name, q or parent, project with fields, or paginate with next.offset".into();
    } else if full_cut {
        res.truncated = true;
        res.hint = format!(
            "full detail returns at most {MAX_FULL_ITEMS} items: narrow with name or q, or paginate with next.offset"
        );
    } else if res.next.is_some() && res.total > 3 * q.limit {
        res.hint =
            "many matches: narrow with kind, name, q or parent, or paginate with next.offset"
                .into();
    }
    if !res.counts.is_empty()
        && q.kind.is_empty()
        && q.name.is_empty()
        && q.q.is_empty()
        && q.parent.is_empty()
    {
        res.hint = format!(
            "overview by kind: zoom with kind (and name or q), then detail summary or full. {}",
            res.hint
        )
        .trim()
        .to_string();
    }
    Ok(res)
}

impl Query {
    /// validated checks and completes the query.
    pub(crate) fn validated(mut self) -> Result<Query, String> {
        if !self.kind.is_empty() && !ELEMENT_KINDS.contains(&self.kind.as_str()) {
            return Err(format!(
                "unknown kind {}: the kinds are {}",
                q(&self.kind),
                ELEMENT_KINDS.join(", ")
            ));
        }
        match self.detail.as_str() {
            "" => {
                self.detail = DETAIL_SUMMARY.into();
                if self.kind.is_empty()
                    && self.name.is_empty()
                    && self.q.is_empty()
                    && self.parent.is_empty()
                {
                    // the overview: the counts say what exists, the names are enough to zoom
                    self.detail = DETAIL_NAMES.into();
                }
            }
            DETAIL_NAMES | DETAIL_SUMMARY | DETAIL_FULL => {}
            _ => {
                return Err(format!(
                    "unknown detail {}: names, summary or full",
                    q(&self.detail)
                ));
            }
        }
        if !self.fields.is_empty() {
            let valid = valid_fields(&self.kind);
            let bad: Vec<&str> = self
                .fields
                .iter()
                .filter(|f| !valid.contains(&f.as_str()))
                .map(String::as_str)
                .collect();
            if !bad.is_empty() {
                let where_ = if self.kind.is_empty() {
                    "for any kind".to_string()
                } else {
                    format!("for kind {}", self.kind)
                };
                return Err(format!(
                    "unknown fields {}: the valid fields {where_} are {}",
                    bad.join(", "),
                    valid.join(", ")
                ));
            }
        }
        if self.limit < 0 || self.offset < 0 {
            return Err("limit and offset cannot be negative".into());
        }
        if self.limit == 0 {
            self.limit = DEFAULT_QUERY_LIMIT;
        }
        self.limit = self.limit.min(MAX_QUERY_LIMIT);
        Ok(self)
    }

    fn matches(&self, e: &Element) -> bool {
        if !self.kind.is_empty() && e.kind != self.kind {
            return false;
        }
        if !self.name.is_empty()
            && !glob(&self.name, &e.name)
            && !(!e.short.is_empty() && glob(&self.name, &e.short))
        {
            return false;
        }
        if !self.q.is_empty() && !e.search.contains(&self.q.to_lowercase()) {
            return false;
        }
        if !self.parent.is_empty()
            && !e
                .parents
                .iter()
                .any(|p| p.to_lowercase() == self.parent.to_lowercase())
        {
            return false;
        }
        true
    }
}

/// glob matches name against a pattern with * and ?, case-insensitively.
pub(crate) fn glob(pattern: &str, name: &str) -> bool {
    let p: Vec<char> = pattern.to_lowercase().chars().collect();
    let s: Vec<char> = name.to_lowercase().chars().collect();
    let (mut pi, mut si, mut mark) = (0usize, 0usize, 0usize);
    let mut star: Option<usize> = None;
    while si < s.len() {
        if pi < p.len() && (p[pi] == '?' || p[pi] == s[si]) {
            pi += 1;
            si += 1;
        } else if pi < p.len() && p[pi] == '*' {
            star = Some(pi);
            mark = si;
            pi += 1;
        } else if let Some(st) = star {
            pi = st + 1;
            mark += 1;
            si = mark;
        } else {
            return false;
        }
    }
    while pi < p.len() && p[pi] == '*' {
        pi += 1;
    }
    pi == p.len()
}

impl Element {
    /// render is the item of an element for the query: name only; a projection; the summary; the whole definition.
    fn render(&self, q: &Query) -> Map<String, Value> {
        let mut out = Map::new();
        out.insert("name".into(), json!(self.name));
        if !q.fields.is_empty() {
            out.insert("kind".into(), json!(self.kind));
            for f in &q.fields {
                if let Some(v) = self.full.get(f)
                    && f != "name"
                {
                    out.insert(f.clone(), shorten(v, FULL_TEXT));
                }
            }
        } else if q.detail == DETAIL_NAMES {
            out.insert("kind".into(), json!(self.kind));
        } else if q.detail == DETAIL_SUMMARY {
            out.insert("kind".into(), json!(self.kind));
            if !self.desc.is_empty() {
                out.insert(
                    "description".into(),
                    json!(clip(first_line(&self.desc), SUMMARY_TEXT)),
                );
            }
            for f in self.summary {
                if let Some(v) = self.full.get(*f) {
                    out.insert((*f).into(), short_list(shorten(v, SUMMARY_TEXT)));
                }
            }
        } else {
            for (k, v) in &self.full {
                if k != "name" {
                    out.insert(k.clone(), shorten(v, FULL_TEXT));
                }
            }
        }
        out
    }
}

fn first_line(s: &str) -> &str {
    let s = s.trim();
    s.split_once('\n').map_or(s, |(a, _)| a)
}

/// shorten cuts the strings of a value (and of the lists and maps in it).
fn shorten(v: &Value, n: usize) -> Value {
    match v {
        Value::String(x) => Value::String(clip(x, n)),
        Value::Array(x) => Value::Array(x.iter().map(|e| shorten(e, n)).collect()),
        Value::Object(x) => {
            Value::Object(x.iter().map(|(k, e)| (k.clone(), shorten(e, n))).collect())
        }
        v => v.clone(),
    }
}

/// shortList keeps the first items of a list, and says how many were left out.
fn short_list(v: Value) -> Value {
    match v {
        Value::Array(l) if l.len() > SUMMARY_LIST => {
            let more = l.len() - SUMMARY_LIST;
            let mut out: Vec<Value> = l.into_iter().take(SUMMARY_LIST).collect();
            out.push(json!(format!("… +{more} more")));
            Value::Array(out)
        }
        v => v,
    }
}

/// toMap is the JSON form of a definition, which carries its omitempty rules; an action's kind is renamed.
fn to_map<T: Serialize>(v: &T, rename: Option<(&str, &str)>) -> Map<String, Value> {
    let mut m = match serde_json::to_value(v) {
        Ok(Value::Object(m)) => m,
        _ => Map::new(),
    };
    if let Some((from, to)) = rename
        && let Some(x) = m.remove(from)
    {
        m.insert(to.into(), x);
    }
    m
}

fn mk(
    kind: &'static str,
    name: &str,
    parents: Vec<String>,
    desc: &str,
    examples: &[String],
    mut full: Map<String, Value>,
) -> Element {
    full.insert("name".into(), json!(name));
    full.insert("kind".into(), json!(kind));
    if !parents.is_empty() {
        full.insert(FIELD_PARENTS.into(), json!(parents));
    }
    Element {
        kind,
        name: name.into(),
        short: String::new(),
        search: format!("{name}\n{desc}\n{}", examples.join("\n")).to_lowercase(),
        parents,
        desc: desc.into(),
        full,
        summary: summary_fields(kind),
    }
}

/// A list of names as Go encodes it: null when empty.
fn names_value(v: Vec<String>) -> Value {
    if v.is_empty() { Value::Null } else { json!(v) }
}

/// elementsOf lists what a methodology declares, in the order of ElementKinds. Generated agents, actions and goals
/// (the ones of processes and methods) are not listed: they are the processes and methods themselves.
fn elements_of(m: &Methodology) -> Vec<Element> {
    let mut out = Vec::new();
    // action / goal -> agents admitting it
    let mut owners: HashMap<String, Vec<String>> = HashMap::new();
    for ag in &m.agents {
        for a in &ag.actions {
            owners
                .entry(format!("a:{a}"))
                .or_default()
                .push(ag.name.clone());
        }
        for g in &ag.goals {
            owners
                .entry(format!("g:{g}"))
                .or_default()
                .push(ag.name.clone());
        }
    }
    let owner = |k: String| owners.get(&k).cloned().unwrap_or_default();
    for ag in &m.agents {
        out.push(mk(
            KIND_AGENT,
            &ag.name,
            Vec::new(),
            &ag.description,
            &ag.examples,
            to_map(ag, None),
        ));
    }
    for a in &m.actions {
        out.push(mk(
            KIND_ACTION,
            &a.name,
            owner(format!("a:{}", a.name)),
            &a.description,
            &[],
            to_map(a, Some(("kind", FIELD_ACTION_KIND))),
        ));
    }
    for g in &m.goals {
        out.push(mk(
            KIND_GOAL,
            &g.name,
            owner(format!("g:{}", g.name)),
            &g.description,
            &g.examples,
            to_map(g, None),
        ));
    }
    for p in &m.processes {
        let mut f = to_map(p, None);
        f.insert("steps".into(), names_value(step_names(&p.name, &p.steps)));
        out.push(mk(
            KIND_PROCESS,
            &p.name,
            Vec::new(),
            &p.description,
            &p.examples,
            f,
        ));
    }
    for p in &m.processes {
        append_steps(&mut out, &p.name, &p.name, &p.steps);
    }
    for r in &m.roles {
        out.push(mk(
            KIND_ROLE,
            &r.name,
            Vec::new(),
            &r.description,
            &[],
            to_map(r, None),
        ));
    }
    for me in &m.methods {
        let mut f = to_map(me, None);
        f.insert("steps".into(), names_value(step_names(&me.name, &me.steps)));
        out.push(mk(
            KIND_METHOD,
            &me.name,
            Vec::new(),
            &me.description,
            &[],
            f,
        ));
    }
    for me in &m.methods {
        append_steps(&mut out, &me.name, &me.name, &me.steps);
    }
    for ag in &m.agents {
        for t in &ag.triggers {
            out.push(mk(
                KIND_TRIGGER,
                &t.name,
                vec![ag.name.clone()],
                &t.description,
                &[],
                to_map(t, None),
            ));
        }
    }
    for c in &m.conditions {
        out.push(mk(
            KIND_CONDITION,
            &c.name,
            Vec::new(),
            &c.description,
            &[],
            to_map(c, None),
        ));
    }
    for name in &m.imports {
        let mut names: Vec<String> = goap_condition::library(name)
            .unwrap_or_default()
            .into_iter()
            .map(|d| d.name)
            .collect();
        names.sort();
        let mut f = Map::new();
        f.insert("conditions".into(), names_value(names));
        out.push(mk(
            KIND_LIBRARY,
            name,
            Vec::new(),
            "built-in condition library",
            &[],
            f,
        ));
    }
    out
}

fn step_names(prefix: &str, steps: &[Step]) -> Vec<String> {
    steps
        .iter()
        .map(|s| format!("{prefix}/{}", s.name))
        .collect()
}

/// appendSteps adds the steps of a process or method, depth first; a step is named by its path, its parent is the
/// process (or method) or the step above it.
fn append_steps(out: &mut Vec<Element>, parent: &str, prefix: &str, steps: &[Step]) {
    for s in steps {
        let path = format!("{prefix}/{}", s.name);
        let mut f = to_map(s, None);
        if !s.steps.is_empty() {
            f.insert("steps".into(), json!(step_names(&path, &s.steps)));
        }
        f.insert(FIELD_HOW.into(), json!(s.method()));
        let mut e = mk(
            KIND_STEP,
            &path,
            vec![parent.to_string()],
            &s.description,
            &[],
            f,
        );
        e.short = s.name.clone();
        out.push(e);
        append_steps(out, &path, &path, &s.steps);
    }
}
