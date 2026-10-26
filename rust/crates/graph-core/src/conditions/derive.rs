//! The conditions a methodology compiles to (Go's `engine/methodology` compiler, the part that makes the condition
//! set: `compileConditions`, `injectLibraries`, `compileActions` with `compileExpects` and `compileScope`,
//! `compileMethods`, `compileProcesses` with its steps and gates): the declared conditions, those of the libraries it
//! imports, and those generated for its expectations, scoped actions, steps, gates and states. The graph instantiates
//! them from the definition the registry declares (ADR 0163 addendum, what a change follows).
//!
//! A stored version has passed the compiler (the registry refuses to publish one with issues): what refuses a
//! definition is not checked again here, a part the compiler refuses generates nothing.
use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use std::sync::LazyLock;

use serde::Deserialize;
use serde_json::{Map, Value};

use graph_script::conditions::Definition;

type Bools = BTreeMap<String, bool>;

/// A library of conditions (`condition.Library`).
#[derive(Deserialize)]
struct LibraryDef {
    name: String,
    expr: String,
    #[serde(default)]
    description: String,
}

static LIBRARIES: LazyLock<HashMap<String, Vec<LibraryDef>>> = LazyLock::new(|| {
    #[derive(Deserialize)]
    struct File {
        libraries: HashMap<String, Vec<LibraryDef>>,
    }
    serde_json::from_str::<File>(include_str!("libraries.gen.json"))
        .map(|f| f.libraries)
        .unwrap_or_default()
});

/// The conditions of a library, `None` for an unknown one.
pub fn library(name: &str) -> Option<Vec<Definition>> {
    LIBRARIES.get(name).map(|l| {
        l.iter()
            .map(|d| Definition::plain(&d.name, &d.expr, &d.description))
            .collect()
    })
}

/// An item of the checklist of a gate as compiled (`GateItemInfo`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct GateItem {
    pub name: String,
    pub description: String,
    pub role: String,
    pub veto: bool,
    pub when: String,
    pub attest: bool,
    /// The condition the item checks: its name, or the generated `attested:<path>/<name>`.
    pub condition: String,
}

/// A gate as the compiled methodology holds it (`GateInfo`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Gate {
    pub path: String,
    pub transition: String,
    pub accountable: String,
    pub items: Vec<GateItem>,
    pub board: Vec<String>,
    pub rule: String,
    pub question: String,
    pub back: String,
    pub max_duration: String,
    pub freezes: Option<Value>,
    pub method: String,
}

/// What a definition compiles to here: its conditions, the gates of its processes and the entry and exit criteria of
/// each step, by path.
#[derive(Debug, Clone, Default)]
pub struct Derived {
    pub conditions: Vec<Definition>,
    pub gates: Vec<Gate>,
    pub criteria: BTreeMap<String, (Bools, Bools)>,
    /// The goal of each valid method (its exit criteria), by name.
    pub method_goals: BTreeMap<String, Bools>,
    /// The goal generated for each valid process (its exit criteria), by name.
    pub process_goals: BTreeMap<String, Bools>,
    /// The steps each step relies on, directly or through other steps (`Compiled.StepDependencies`, ADR 0109 §5): a
    /// step relies on one whose exit gives a condition its own actions require, with the same value.
    pub step_deps: BTreeMap<String, BTreeSet<String>>,
}

fn s<'a>(m: &'a Map<String, Value>, k: &str) -> &'a str {
    m.get(k).and_then(Value::as_str).unwrap_or_default()
}

fn list<'a>(m: &'a Map<String, Value>, k: &str) -> &'a [Value] {
    m.get(k).and_then(Value::as_array).map_or(&[], Vec::as_slice)
}

fn objects<'a>(m: &'a Map<String, Value>, k: &str) -> impl Iterator<Item = &'a Map<String, Value>> {
    list(m, k).iter().filter_map(Value::as_object)
}

/// A map of conditions to their values, `None` when the field is absent.
fn bools(m: &Map<String, Value>, k: &str) -> Option<Bools> {
    m.get(k)?
        .as_object()
        .map(|o| o.iter().map(|(k, v)| (k.clone(), v.as_bool() == Some(true))).collect())
}

fn strings(m: &Map<String, Value>, k: &str) -> Vec<String> {
    list(m, k)
        .iter()
        .filter_map(Value::as_str)
        .map(str::to_string)
        .collect()
}

/// Adds src to dst; a condition both require with opposite values keeps the first (the compiler refuses it).
fn merge(dst: &mut Bools, src: &Bools) {
    for (k, v) in src {
        dst.entry(k.clone()).or_insert(*v);
    }
}

/// A quoted string, as Go's `%q` writes a name.
fn q(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

/// An action as the compiler keeps it (its copy, with the generated effects).
#[derive(Debug, Clone, Default)]
struct Act {
    name: String,
    scope: String,
    r#where: String,
    pre: Bools,
    effects: Option<Bools>,
    scoped_pre: Bools,
    scoped_effects: Bools,
    lifted: BTreeSet<String>,
    narrowed: bool,
    /// The exit criteria of the step a narrowed action runs for, its effects too (`Action.stepDone`).
    step_done: Bools,
    implements: String,
    specializes: String,
}

impl Act {
    fn is_scoped(&self) -> bool {
        !self.scope.is_empty() && self.scope != "change"
    }

    /// The name of the generated conditions of a scoped action (`scopeName`).
    fn scope_name(&self) -> &str {
        if self.narrowed || self.implements.is_empty() {
            &self.name
        } else {
            &self.implements
        }
    }

    fn pending(&self) -> String {
        format!("pending:{}", self.scope_name())
    }
}

/// The conditions a control's entry needs (`controlsPre`).
fn controls_pre(m: &Map<String, Value>) -> Bools {
    list(m, "controls")
        .iter()
        .filter_map(|c| match c {
            Value::String(s) => Some(s.as_str()),
            Value::Object(o) => o.get("condition").and_then(Value::as_str),
            _ => None,
        })
        .filter(|c| !c.is_empty())
        .map(|c| (c.to_string(), true))
        .collect()
}

/// The CEL expression of an expectation (`condition.Expectation.Expr`), `None` when the compiler refuses it.
fn expectation(e: &Map<String, Value>) -> Option<String> {
    if s(e, "forEach") != "changeImpacts" {
        return None;
    }
    let mut r#where = r#"x.intent == "modified""#.to_string();
    if !s(e, "where").trim().is_empty() {
        r#where += &format!(" && ({})", s(e, "where"));
    }
    let src = format!("changeImpacts.filter(x, {where})");
    let produce = e.get("produce").and_then(Value::as_object).cloned().unwrap_or_default();
    let link = e.get("link").and_then(Value::as_object);
    let matched = match s(&produce, "op") {
        "update_node" => {
            r#"changeImpacts.exists(n, n.intent == "modified" && n.hasPost && n.key == x.key)"#.to_string()
        }
        "create_node" => {
            let link = link.filter(|l| !s(l, "type").is_empty())?;
            let type_check = match s(&produce, "nodeType") {
                "" => String::new(),
                t => format!(" && n.type == {}", q(t)),
            };
            if s(link, "direction") == "in" {
                format!(
                    r#"changeImpacts.exists(n, n.intent == "created" && n.hasPost{type_check} && x.hasPost && x.post.out.exists(l, l.type == {} && l.to.id == n.post.id))"#,
                    q(s(link, "type"))
                )
            } else {
                format!(
                    r#"changeImpacts.exists(n, n.intent == "created" && n.hasPost{type_check} && n.post.out.exists(l, l.type == {} && l.to.id == x.pre.id))"#,
                    q(s(link, "type"))
                )
            }
        }
        _ => return None,
    };
    Some(format!("size({src}) > 0 && {src}.all(x, {matched})"))
}

/// The conjunction of two where expressions (`andWhere`).
fn and_where(a: &str, b: &str) -> String {
    let wrap = |x: &str| {
        if x.contains("||") || x.contains('?') {
            format!("({x})")
        } else {
            x.to_string()
        }
    };
    match (a, b) {
        ("", b) => b.to_string(),
        (a, "") => a.to_string(),
        (a, b) => format!("{} && {}", wrap(a), wrap(b)),
    }
}

/// The state of a compilation: what its phases share (`compileState`).
struct State<'a> {
    m: &'a Map<String, Value>,
    defs: Vec<Definition>,
    known: HashSet<String>,
    scopes: HashMap<String, String>,
    /// The declared aggregates over a scoped condition: name to (operator, condition, where).
    aggregates: BTreeMap<String, (&'static str, String, String)>,
    actions: HashMap<String, Act>,
}

impl State<'_> {
    fn define(&mut self, d: Definition) {
        self.known.insert(d.name.clone());
        self.defs.push(d);
    }

    /// The declared conditions: those of the change first, then the scoped ones and the aggregates.
    fn conditions(&mut self) {
        let mut scoped = Vec::new();
        for c in objects(self.m, "conditions") {
            let name = s(c, "name");
            if name.is_empty() || self.known.contains(name) {
                continue;
            }
            self.known.insert(name.to_string());
            let d = Definition {
                name: name.into(),
                expr: s(c, "expr").into(),
                description: s(c, "description").into(),
                scope: s(c, "scope").into(),
                r#where: s(c, "where").into(),
                all: s(c, "all").into(),
                any: s(c, "any").into(),
                none: s(c, "none").into(),
                empty: c.get("empty").and_then(Value::as_bool) == Some(true),
            };
            if d.is_scoped() || d.is_aggregate() {
                if d.is_scoped() {
                    self.scopes.insert(d.name.clone(), d.scope.clone());
                }
                scoped.push(d);
                continue;
            }
            self.defs.push(d);
        }
        for d in &scoped {
            let (op, of) = d.aggregate();
            if !op.is_empty() && self.scopes.contains_key(of) {
                self.aggregates
                    .insert(d.name.clone(), (op, of.to_string(), d.r#where.clone()));
            }
        }
        self.defs.extend(scoped);
    }

    /// The conditions of the libraries the methodology imports, unless it declares its own.
    fn libraries(&mut self) {
        let mut seen = HashSet::new();
        for name in strings(self.m, "imports") {
            let Some(lib) = library(&name) else { continue };
            if !seen.insert(name) {
                continue;
            }
            for d in lib {
                if !self.known.contains(&d.name) {
                    self.define(d);
                }
            }
        }
    }

    fn actions(&mut self) {
        for src in objects(self.m, "actions") {
            let name = s(src, "name");
            if name.is_empty() || self.actions.contains_key(name) {
                continue;
            }
            let mut a = Act {
                name: name.into(),
                scope: s(src, "scope").into(),
                r#where: s(src, "where").into(),
                pre: bools(src, "pre").unwrap_or_default(),
                effects: bools(src, "effects"),
                specializes: s(src, "specializes").into(),
                ..Act::default()
            };
            merge(&mut a.pre, &controls_pre(src));
            if let Some(expr) = src.get("expects").and_then(Value::as_object).and_then(expectation) {
                let cond = format!("expect:{name}");
                self.define(Definition::plain(
                    &cond,
                    expr,
                    format!("what the action {name} is expected to produce is there"),
                ));
                a.effects.get_or_insert_with(Bools::new).insert(cond, true);
            }
            let a = self.scope(a);
            self.actions.insert(name.into(), a);
        }
    }

    /// Lifts a scoped action for the planner (`compileScope`): the scoped condition of a pending subject and the
    /// condition of the change saying one is pending.
    fn scope(&mut self, mut a: Act) -> Act {
        if !a.is_scoped() {
            return a;
        }
        let split = |set: &Bools| -> Option<(Bools, Bools)> {
            let (mut scoped, mut change) = (Bools::new(), Bools::new());
            for (k, v) in set {
                match self.scopes.get(k).map(String::as_str) {
                    None | Some("") => change.insert(k.clone(), *v),
                    Some(sc) if sc == a.scope => scoped.insert(k.clone(), *v),
                    Some(_) => return None,
                };
            }
            Some((scoped, change))
        };
        let (Some((pre, mut change_pre)), Some((eff, mut change_eff))) =
            (split(&a.pre), split(&a.effects.clone().unwrap_or_default()))
        else {
            return a;
        };
        if eff.is_empty() {
            return a;
        }
        let term = |k: &str, v: bool| format!("({} in self.conditions && self.conditions[{}] == {v})", q(k), q(k));
        let have: Vec<String> = pre.iter().map(|(k, v)| term(k, *v)).collect();
        let want: Vec<String> = eff.iter().map(|(k, v)| term(k, *v)).collect();
        let mut expr = format!("!({})", want.join(" && "));
        if !have.is_empty() {
            expr = format!("{} && {expr}", have.join(" && "));
        }
        let (subject, pending) = (format!("subject:{}", a.scope_name()), a.pending());
        self.define(Definition {
            name: subject.clone(),
            scope: a.scope.clone(),
            r#where: a.r#where.clone(),
            expr,
            description: format!("the action {} is still to be done for this {}", a.scope_name(), a.scope),
            ..Definition::default()
        });
        self.define(Definition {
            name: pending.clone(),
            any: subject.clone(),
            description: format!("the action {} is still to be done for some {}", a.scope_name(), a.scope),
            ..Definition::default()
        });
        self.scopes.insert(subject, a.scope.clone());
        change_pre.insert(pending, true);
        a.lifted = BTreeSet::new();
        for (k, v) in &eff {
            for agg in self.lifted(k, *v, &a.r#where) {
                change_eff.insert(agg.clone(), true);
                a.lifted.insert(agg);
            }
        }
        a.scoped_pre = pre;
        a.scoped_effects = eff;
        a.pre = change_pre;
        a.effects = Some(change_eff);
        a
    }

    /// Lets the planner chain two scoped actions of one scope (`chainScoped`, ADR 0118): when what one makes true on
    /// its subject is what another needs on its own, running the first is planned to leave a subject pending for the
    /// second, its pending condition joining the effects of the first, for the plan only (lifted, never checked once
    /// it ran).
    fn chain_scoped(&mut self) {
        let mut names: Vec<String> = self.actions.keys().cloned().collect();
        names.sort();
        for name in &names {
            let a = &self.actions[name];
            if !a.is_scoped() || a.scoped_effects.is_empty() {
                continue;
            }
            let pendings: Vec<String> = names
                .iter()
                .filter(|other| *other != name)
                .map(|other| &self.actions[other])
                .filter(|b| b.is_scoped() && b.scope == a.scope && !b.scoped_effects.is_empty())
                .filter(|b| a.scoped_effects.iter().any(|(k, v)| b.scoped_pre.get(k) == Some(v)))
                .map(Act::pending)
                .collect();
            let a = self.actions.get_mut(name).expect("one of its own names");
            for p in pendings {
                a.effects.get_or_insert_with(Bools::new).insert(p.clone(), true);
                a.lifted.insert(p);
            }
        }
    }

    /// The aggregates a scoped effect is lifted to (`liftedAggregates`).
    fn lifted(&self, cond: &str, value: bool, r#where: &str) -> Vec<String> {
        let (mut within, mut plain) = (Vec::new(), Vec::new());
        for (name, (op, of, w)) in &self.aggregates {
            if of != cond || (value && *op != "all") || (!value && *op != "none") {
                continue;
            }
            if w == r#where
                || (!r#where.is_empty() && w.starts_with(&format!("{where} && ")))
                || (r#where.is_empty() && !w.is_empty())
            {
                within.push(name.clone());
            } else if w.is_empty() {
                plain.push(name.clone());
            }
        }
        if within.is_empty() {
            within = plain;
        }
        within.sort();
        within
    }
}

/// A walk of the steps of the processes or of the methods (`stepWalker`).
struct Walker<'a, 'b> {
    st: &'b mut State<'a>,
    /// The goals of the valid methods, in declaration order, with the capability each provides; empty for the
    /// steps of a method, which name no capability.
    methods: Vec<(String, String, Bools)>,
    /// The gate of each transition; `None` while walking methods, which have none.
    gates: Option<HashMap<String, String>>,
    infos: Vec<Gate>,
    refs: Vec<String>,
    narrowed: Vec<Act>,
    criteria: BTreeMap<String, (Bools, Bools)>,
    done: HashMap<String, Option<Bools>>,
    /// What the actions generated for each step require and make true, unions by path: what the dependencies between
    /// the steps are read from (`Compiled.StepDependencies` over the actions carrying a `Step`).
    acts: BTreeMap<String, (Bools, Bools)>,
}

impl Walker<'_, '_> {
    /// Records what an action generated for a step requires and makes true.
    fn record(&mut self, path: &str, pre: &Bools, exit: &Bools) {
        let e = self.acts.entry(path.to_string()).or_default();
        merge(&mut e.0, pre);
        merge(&mut e.1, exit);
    }

    fn walk(&mut self, steps: &[Value], prefix: &str, inherited: &Bools) -> Bools {
        let mut all = Bools::new();
        let mut seen = HashSet::new();
        for step in steps.iter().filter_map(Value::as_object) {
            let name = s(step, "name");
            if name.is_empty() || !seen.insert(name) {
                continue;
            }
            let (pre, done) = (
                bools(step, "pre").unwrap_or_default(),
                bools(step, "done").unwrap_or_default(),
            );
            self.refs.extend(pre.keys().chain(done.keys()).cloned());
            let mut need = inherited.clone();
            merge(&mut need, &pre);
            merge(&mut need, &controls_pre(step));
            let path = format!("{prefix}/{name}");
            let d = self.step(step, &path, &need).unwrap_or_default();
            merge(&mut all, &d);
            self.criteria.insert(path, (need, d));
        }
        all
    }

    /// One step; its exit criteria (`None` for a step the compiler refuses, or done by an action with no effect).
    fn step(&mut self, step: &Map<String, Value>, path: &str, need: &Bools) -> Option<Bools> {
        if step.get("gate").is_some_and(Value::is_object) {
            self.gates.as_ref()?;
            return self.gate(step, path, need);
        }
        let (foreach, capability) = (s(step, "foreach"), s(step, "method"));
        if (!s(step, "groupBy").is_empty() && foreach.is_empty()) || (!foreach.is_empty() && capability.is_empty()) {
            return None;
        }
        let steps = list(step, "steps");
        let actions = strings(step, "actions");
        let (action, process) = (s(step, "action"), s(step, "process"));
        let ways = [
            !steps.is_empty(),
            !action.is_empty(),
            !actions.is_empty(),
            !process.is_empty(),
            !capability.is_empty(),
        ];
        if ways.iter().filter(|w| **w).count() > 1 {
            return None;
        }
        let mut done = bools(step, "done");
        if !steps.is_empty() {
            let sub = self.walk(steps, path, need);
            let mut done = done.unwrap_or_default();
            merge(&mut done, &sub);
            return Some(done);
        }
        if !action.is_empty() || !actions.is_empty() {
            let names = if action.is_empty() {
                actions
            } else {
                vec![action.to_string()]
            };
            let r#where = s(step, "where");
            for name in &names {
                let a = self.st.actions.get(name)?.clone();
                if !a.specializes.is_empty() {
                    return None;
                }
                if done.is_none() {
                    done = a.effects.clone();
                }
                let mut alt = a.clone();
                alt.name = if names.len() > 1 {
                    format!("{path}:{name}")
                } else {
                    path.to_string()
                };
                alt.implements = name.clone();
                alt.pre = need.clone();
                merge(&mut alt.pre, &a.pre);
                let scoped = !r#where.is_empty();
                if scoped && (!a.is_scoped() || bools(step, "done").is_none_or(|d| d.is_empty())) {
                    return None;
                }
                if scoped {
                    alt = narrow(alt, r#where, need);
                }
                let exit = alt.effects.get_or_insert_with(Bools::new);
                merge(exit, done.as_ref().unwrap_or(&Bools::new()));
                if alt.narrowed {
                    // its subjects are narrowed: it is lifted again over them once the steps are compiled, and what
                    // it requires and makes true is read from that form
                    alt.step_done = done.clone().unwrap_or_default();
                    self.narrowed.push(alt);
                } else {
                    let (pre, exit) = (alt.pre.clone(), exit.clone());
                    self.record(path, &pre, &exit);
                }
            }
            return done;
        }
        if !process.is_empty() {
            let (other, proc) = process.split_once('/').unwrap_or(("", process));
            let own = s(self.st.m, "name");
            if other.is_empty() || other == own {
                let i = objects(self.st.m, "processes").position(|p| s(p, "name") == proc)?;
                if let Some(c) = self.process(i)
                    && done.is_none()
                {
                    done = Some(c);
                }
            }
        } else if !capability.is_empty() {
            let candidates: Vec<&Bools> = self
                .methods
                .iter()
                .filter(|(_, f, _)| f == capability)
                .map(|(_, _, g)| g)
                .collect();
            if candidates.is_empty() {
                return None;
            }
            if foreach.is_empty() && done.is_none() {
                let shared = shared_criteria(&candidates);
                if shared.is_empty() {
                    return None;
                }
                done = Some(shared);
            }
        }
        if done.is_none() {
            // done once it has run, and its run still stands: the history of the change says so (ADR 0109 §5)
            let name = format!("step:{path}");
            if !self.st.known.contains(&name) {
                self.st.define(Definition::plain(
                    &name,
                    format!("{} in history.last && history.last[{}].current", q(path), q(path)),
                    format!("the step {path} has run, and what it did still stands"),
                ));
            }
            done = Some(Bools::from([(name, true)]));
        }
        if let Some(d) = &done {
            self.record(path, need, d);
        }
        done
    }

    /// A process of the methodology, generated once: its exit criteria (`None` when it is invalid).
    fn process(&mut self, i: usize) -> Option<Bools> {
        let p = objects(self.st.m, "processes").nth(i)?;
        let name = s(p, "name").to_string();
        if let Some(c) = self.done.get(&name) {
            return c.clone();
        }
        self.done.insert(name.clone(), None);
        let steps = list(p, "steps");
        if steps.is_empty() {
            return None;
        }
        let done = self.walk(steps, &name, &Bools::new());
        if done.is_empty() {
            return None;
        }
        self.done.insert(name, Some(done.clone()));
        Some(done)
    }

    /// A gate step (`stepWalker.gate`): the conditions of its passage, of its decision and of its attested items.
    fn gate(&mut self, step: &Map<String, Value>, path: &str, need: &Bools) -> Option<Bools> {
        let g = step.get("gate").and_then(Value::as_object)?;
        let transition = s(g, "transition");
        let busy = !list(step, "steps").is_empty()
            || !s(step, "action").is_empty()
            || !list(step, "actions").is_empty()
            || !s(step, "process").is_empty()
            || !s(step, "foreach").is_empty()
            || !s(step, "where").is_empty()
            || step.get("review").is_some_and(|r| !r.is_null());
        if busy
            || s(self.st.m, "lifecycle").is_empty()
            || transition.is_empty()
            || bools(step, "done").is_some_and(|d| !d.is_empty())
            || self.gates.as_ref().is_some_and(|gs| gs.contains_key(transition))
        {
            return None;
        }
        let roles = step.get("roles").and_then(Value::as_object);
        let accountable = roles.map(|r| s(r, "accountable")).unwrap_or_default();
        if accountable.is_empty() {
            return None;
        }
        let name = s(step, "name");
        let mut info = Gate {
            path: path.into(),
            transition: transition.into(),
            accountable: accountable.into(),
            freezes: g.get("freezes").filter(|f| !f.is_null()).cloned(),
            method: s(step, "method").into(),
            question: format!("Go for {name}?"),
            ..Gate::default()
        };
        if !s(step, "description").is_empty() {
            info.question = format!("Go for {name} ({})?", s(step, "description").trim_end_matches('.'));
        }
        if let Some(d) = g.get("decision").and_then(Value::as_object) {
            if !s(d, "question").is_empty() {
                info.question = s(d, "question").into();
            }
            (info.back, info.rule, info.max_duration) =
                (s(d, "back").into(), s(d, "rule").into(), s(d, "maxDuration").into());
            info.board = strings(d, "board");
        }
        if info.board.is_empty() {
            info.board = vec![accountable.into()];
        } else if !info.board.iter().any(|r| r == accountable) {
            info.board.push(accountable.into());
        }
        let mut seen = HashSet::new();
        for it in objects(g, "checklist") {
            let item = s(it, "name");
            if item.is_empty() || !seen.insert(item) {
                continue;
            }
            let role = match s(it, "role") {
                "" => accountable,
                r => r,
            };
            let attest = it.get("attest").and_then(Value::as_bool) == Some(true);
            let mut cond = item.to_string();
            if attest {
                cond = format!("attested:{path}/{item}");
                let key = format!("{path}/{item}");
                let what = match s(it, "description") {
                    "" => item,
                    d => d,
                };
                self.st.define(Definition::plain(
                    &cond,
                    format!(
                        r#"{} in attestations && attestations[{}].verdict == "met""#,
                        q(&key),
                        q(&key)
                    ),
                    format!("a {role} attested {what} for the gate {path}"),
                ));
            } else {
                self.refs.push(cond.clone());
            }
            info.items.push(GateItem {
                name: item.into(),
                description: s(it, "description").into(),
                role: role.into(),
                veto: it.get("veto").and_then(Value::as_bool) == Some(true),
                when: s(it, "when").into(),
                attest,
                condition: cond,
            });
        }
        for (n, expr, description) in [
            (
                format!("gate:{path}"),
                format!("{} in change.passed", q(transition)),
                format!("the gate {path} is passed: the change took {transition} and has not gone back since"),
            ),
            (
                format!("gate_open:{path}"),
                format!(
                    r#"decisionPoints.exists(d, "gate" in d.policy && d.policy.gate == {} && d.status != "decided")"#,
                    q(path)
                ),
                format!("the decision of the gate {path} is open, waiting for its rulings"),
            ),
            (
                format!("gate_go:{path}"),
                format!(
                    r#"decisionPoints.exists(d, "gate" in d.policy && d.policy.gate == {} && d.status == "decided" && d.option == "go" && !(d.id in change.consumed))"#,
                    q(path)
                ),
                format!("the decision of the gate {path} said go"),
            ),
        ] {
            self.st.define(Definition::plain(n, expr, description));
        }
        if !info.method.is_empty() && !self.methods.iter().any(|(_, f, _)| *f == info.method) {
            return None;
        }
        if let Some(gs) = self.gates.as_mut() {
            gs.insert(transition.into(), path.into());
        }
        // what the actions of the gate require: the entry of the step and the applicable vetos of its checklist,
        // needed before its decision is opened (`open`); what they make true is its own passage
        let mut pre = need.clone();
        for it in info.items.iter().filter(|it| it.veto && it.when.is_empty()) {
            pre.insert(it.condition.clone(), true);
        }
        let done = Bools::from([(format!("gate:{path}"), true)]);
        self.record(path, &pre, &done);
        self.infos.push(info);
        Some(done)
    }
}

/// The action of a step running a scoped action over a part of its subjects (`narrow`).
fn narrow(mut a: Act, r#where: &str, need: &Bools) -> Act {
    let pending = a.pending();
    let mut pre = need.clone();
    for (k, v) in &a.pre {
        if *k != pending {
            pre.insert(k.clone(), *v);
        }
    }
    pre.extend(a.scoped_pre.clone());
    let mut eff: Bools = a
        .effects
        .clone()
        .unwrap_or_default()
        .into_iter()
        .filter(|(k, _)| !a.lifted.contains(k))
        .collect();
    eff.extend(a.scoped_effects.clone());
    a.pre = pre;
    a.effects = Some(eff);
    a.r#where = and_where(&a.r#where, r#where);
    a.narrowed = true;
    a.scoped_pre.clear();
    a.scoped_effects.clear();
    a.lifted.clear();
    a
}

/// The steps each step relies on, directly or through other steps (`Compiled.StepDependencies`, ADR 0109 §5), from
/// what the actions of each step require and make true: a step relies on another when one of its own requirements is
/// an exit of that other with the same value. The entry of a step naming a nested process does not carry what that
/// process needs from outside, so a dependency only that would show is not seen.
fn step_deps(acts: &BTreeMap<String, (Bools, Bools)>) -> BTreeMap<String, BTreeSet<String>> {
    let mut direct: BTreeMap<&str, Vec<&str>> = BTreeMap::new();
    for (later, (pre, _)) in acts {
        for (earlier, (_, exit)) in acts.iter().filter(|(e, _)| *e != later) {
            if pre.iter().any(|(k, v)| exit.get(k) == Some(v)) {
                direct.entry(later).or_default().push(earlier);
            }
        }
    }
    let mut out = BTreeMap::new();
    for later in direct.keys() {
        let mut seen: BTreeSet<String> = BTreeSet::new();
        let mut todo = vec![*later];
        while let Some(p) = todo.pop() {
            for e in direct.get(p).map(Vec::as_slice).unwrap_or_default() {
                if *e != *later && seen.insert((*e).to_string()) {
                    todo.push(e);
                }
            }
        }
        out.insert((*later).to_string(), seen);
    }
    out
}

/// The conditions every method's goal makes true with the same value (`sharedCriteria`).
fn shared_criteria(goals: &[&Bools]) -> Bools {
    let mut out: Option<Bools> = None;
    for g in goals {
        match &mut out {
            None => out = Some((*g).clone()),
            Some(o) => o.retain(|k, v| g.get(k) == Some(v)),
        }
    }
    out.unwrap_or_default()
}

/// What a definition (the JSON of a methodology) compiles to.
pub fn derive(m: &Map<String, Value>) -> Derived {
    let mut st = State {
        m,
        defs: Vec::new(),
        known: HashSet::new(),
        scopes: HashMap::new(),
        aggregates: BTreeMap::new(),
        actions: HashMap::new(),
    };
    st.conditions();
    st.libraries();
    st.actions();
    st.chain_scoped();
    let mut out = Derived::default();
    // the methods, before the processes that name their capabilities
    let mut methods = Vec::new();
    let mut narrowed = Vec::new();
    let mut acts: BTreeMap<String, (Bools, Bools)> = BTreeMap::new();
    {
        let mut w = Walker {
            st: &mut st,
            methods: Vec::new(),
            gates: None,
            infos: Vec::new(),
            refs: Vec::new(),
            narrowed: Vec::new(),
            criteria: BTreeMap::new(),
            done: HashMap::new(),
            acts: BTreeMap::new(),
        };
        let mut seen = HashSet::new();
        for me in objects(m, "methods") {
            let name = s(me, "name");
            if name.is_empty() || !seen.insert(name) {
                continue;
            }
            let steps = list(me, "steps");
            let goal = if !steps.is_empty() {
                w.walk(steps, name, &Bools::new())
            } else if !list(me, "actions").is_empty() {
                bools(me, "done").unwrap_or_default()
            } else {
                continue;
            };
            if goal.is_empty() {
                continue;
            }
            out.method_goals.insert(name.into(), goal.clone());
            methods.push((name.to_string(), s(me, "for").to_string(), goal));
        }
        narrowed.append(&mut w.narrowed);
        out.criteria.append(&mut w.criteria);
        acts.append(&mut w.acts);
    }
    let mut w = Walker {
        st: &mut st,
        methods,
        gates: Some(HashMap::new()),
        infos: Vec::new(),
        refs: Vec::new(),
        narrowed: Vec::new(),
        criteria: BTreeMap::new(),
        done: HashMap::new(),
        acts: BTreeMap::new(),
    };
    let mut process_goals = BTreeMap::new();
    for i in 0..objects(m, "processes").count() {
        if let Some(goal) = w.process(i)
            && let Some(p) = objects(m, "processes").nth(i)
        {
            process_goals.insert(s(p, "name").to_string(), goal);
        }
    }
    out.process_goals = process_goals;
    let refs = std::mem::take(&mut w.refs);
    let infos = std::mem::take(&mut w.infos);
    narrowed.append(&mut w.narrowed);
    out.criteria.append(&mut w.criteria);
    acts.append(&mut w.acts);
    for r in refs {
        // "state:<name>" is generated (ADR 0058): true while the change is in that state of its lifecycle
        if let Some(state) = r.strip_prefix("state:")
            && !state.is_empty()
            && !st.known.contains(&r)
        {
            st.define(Definition::plain(
                &r,
                format!("change.state == {}", q(state)),
                format!("the change is in the state {state} of its lifecycle"),
            ));
        }
    }
    for a in narrowed {
        // a step narrowing the subjects of a scoped action: lifted again over its own subjects
        let a = st.scope(a);
        let mut exit = a.effects.clone().unwrap_or_default();
        merge(&mut exit, &a.step_done);
        let e: &mut (Bools, Bools) = acts.entry(a.name.clone()).or_default();
        merge(&mut e.0, &a.pre);
        merge(&mut e.1, &exit);
    }
    out.step_deps = step_deps(&acts);
    out.conditions = st.defs;
    out.gates = infos;
    out
}
