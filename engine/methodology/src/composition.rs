//! The check of the composition of methodologies (ADR 0106, `composition.go`): what a methodology expects of the
//! methodologies it is composed with (the events it follows through `on.from`, the states and transitions its
//! guardian names, the guards the chain of guardians adds up) holds only against their definitions. Every issue is a
//! warning carrying a hint.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::{Arc, LazyLock};

use goap_def::{Issue, Issues, NAME_RE, SEVERITY_WARNING};
use goap_domain::Lifecycle;
use goap_guard::{Facts, Guard};
use regex::Regex;
use serde_json::{Map, Value, json};

use crate::event::standard_event;
use crate::integrate::{
    ANY_METHODOLOGY, FROM_DEPENDENCY, FROM_INTEGRATED, FROM_INTEGRATOR, FROM_SELF,
};
use crate::method::Subscription;
use crate::methodology::Methodology;
use crate::process::Step;
use crate::util::q;

/// The lifecycle the changes of a methodology follow (None: none, or unknown).
pub type LifecycleOf = Arc<dyn Fn(&Methodology) -> Option<Lifecycle> + Send + Sync>;

/// Composition is what the check of a methodology's composition knows of the others.
#[derive(Clone, Default)]
pub struct Composition {
    /// Others is the latest version of each other methodology, by name.
    pub others: BTreeMap<String, Methodology>,
    /// Lifecycle resolves the lifecycle the changes of a methodology follow (None: none).
    pub lifecycle: Option<LifecycleOf>,
}

/// maxComposedChain bounds the walk up the integrators.
const MAX_COMPOSED_CHAIN: usize = 8;

/// maxWorldRefs bounds the conditions of the world a guard may read for its satisfiability to be judged.
const MAX_WORLD_REFS: usize = 4;

impl Composition {
    fn lifecycle_of(&self, m: &Methodology) -> Option<Lifecycle> {
        match &self.lifecycle {
            Some(f) if !m.lifecycle.is_empty() => f(m),
            _ => None,
        }
    }
}

impl Methodology {
    /// CompositionIssues checks m against the methodologies it is composed with. Every issue is a warning carrying a
    /// Hint.
    pub fn composition_issues(&self, c: &Composition) -> Issues {
        let mut ck = Check {
            m: self,
            c,
            out: Vec::new(),
        };
        ck.integrations();
        for ps in self.all_subscriptions() {
            ck.subscription(&ps.path, &ps.sub);
        }
        ck.guardian();
        ck.consumers();
        ck.composite();
        Issues(ck.out)
    }
}

struct Check<'a> {
    m: &'a Methodology,
    c: &'a Composition,
    out: Vec<Issue>,
}

struct Finding {
    msg: String,
    hint: String,
}

/// framed is a methodology whose changes the guardian of m frames.
struct Framed<'a> {
    m: &'a Methodology,
    /// between are the guardians from its integrator up to m, m left out, innermost first.
    between: Vec<&'a Methodology>,
}

impl Framed<'_> {
    /// via is the methodology m integrates on the way down to x (x itself when m integrates it).
    fn via(&self, x: &Methodology) -> String {
        match self.between.last() {
            None => x.name.clone(),
            Some(b) => b.name.clone(),
        }
    }
}

#[allow(clippy::enum_variant_names)]
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Sat {
    Unknown,
    Sat,
    Unsat,
}

/// level is one guardian of a chain over a change: the guard it adds and the methodology its integrator is a change
/// of.
struct Level<'a> {
    expr: String,
    guardian: &'a Methodology,
}

impl<'a> Check<'a> {
    fn add(&mut self, path: impl Into<String>, hint: impl Into<String>, msg: impl Into<String>) {
        let mut is = Issue::new(path, msg);
        is.hint = hint.into();
        is.severity = SEVERITY_WARNING.into();
        self.out.push(is);
    }

    fn other(&self, name: &str) -> Option<&'a Methodology> {
        if name == self.m.name {
            return Some(self.m);
        }
        self.c.others.get(name)
    }

    /// integrated are the known methodologies m integrates (the wildcard: unknown, wild true).
    fn integrated(&self) -> (Vec<&'a Methodology>, bool) {
        let mut out: Vec<&'a Methodology> = Vec::new();
        let mut wild = false;
        for inn in &self.m.integrates {
            for n in &inn.methodologies {
                if n == ANY_METHODOLOGY {
                    wild = true;
                } else if let Some(o) = self.c.others.get(n)
                    && !out.iter().any(|x| std::ptr::eq(*x, o))
                {
                    out.push(o);
                }
            }
        }
        (out, wild)
    }

    /// integrators are the known methodologies integrating the changes of the methodology named name, by name order.
    fn integrators(&self, name: &str) -> Vec<&'a Methodology> {
        let mut out: Vec<&'a Methodology> = Vec::new();
        for n in self.other_names() {
            let o = &self.c.others[&n];
            if o.integrates_changes_of(name) {
                out.push(o);
            }
        }
        if name != self.m.name && self.m.integrates_changes_of(name) {
            out.push(self.m);
        }
        out
    }

    fn other_names(&self) -> Vec<String> {
        self.c
            .others
            .keys()
            .filter(|n| **n != self.m.name)
            .cloned()
            .collect()
    }

    /// integrations: a methodology integrates names no known methodology.
    fn integrations(&mut self) {
        let known = self.other_names();
        let m = self.m;
        for (i, inn) in m.integrates.iter().enumerate() {
            for (j, n) in inn.methodologies.iter().enumerate() {
                if n == ANY_METHODOLOGY
                    || *n == m.name
                    || !NAME_RE.is_match(n)
                    || self.c.others.contains_key(n)
                {
                    continue;
                }
                self.add(
                    format!("integrates[{i}].methodologies[{j}]"),
                    suggest(n, &known, "known methodologies"),
                    format!("no methodology {n} is known: nothing is integrated through this name"),
                );
            }
        }
    }

    // --- events ---------------------------------------------------------------------------------------------------

    /// sources are the methodologies the events of a subscription come from, by its origin; false when they cannot
    /// be known.
    fn sources(&self, sub: &Subscription) -> (Vec<&'a Methodology>, bool) {
        match sub.origin() {
            FROM_INTEGRATED => {
                let (inn, wild) = self.integrated();
                (inn, !wild)
            }
            FROM_INTEGRATOR => (self.integrators(&self.m.name), true),
            FROM_DEPENDENCY => (Vec::new(), false),
            _ => {
                if self.m.applies_to.is_empty() {
                    return (vec![self.m], true);
                }
                let out: Vec<&'a Methodology> = self
                    .m
                    .applies_to
                    .iter()
                    .filter_map(|n| self.other(n))
                    .collect();
                let ok = !out.is_empty();
                (out, ok)
            }
        }
    }

    /// emits reports whether the changes (or the processes) of src can raise the event; when not, why and how to fix
    /// it.
    fn emits(&self, src: &Methodology, event: &str) -> (bool, String, String) {
        match event {
            goap_events::CHANGE_TRANSITIONED => {
                if self.c.lifecycle_of(src).is_none() {
                    return (
                        false,
                        format!(
                            "{} names no lifecycle: its changes never transition",
                            src.name
                        ),
                        format!(
                            "give {} a lifecycle (lifecycle: <a lifecycle of its domain>), or follow {} instead",
                            src.name,
                            goap_events::CHANGE_COMMITTED
                        ),
                    );
                }
            }
            goap_events::STEP_COMPLETED
            | goap_events::PROCESS_ATTACHED
            | goap_events::PROCESS_COMPLETED
            | goap_events::PROCESS_FAILED
            | goap_events::PROCESS_STUCK => {
                if src.processes.is_empty() && src.methods.is_empty() && src.agents.is_empty() {
                    return (
                        false,
                        format!("{} runs no process on its changes", src.name),
                        format!(
                            "declare a process in {}, or follow a change event ({}, {})",
                            src.name,
                            goap_events::CHANGE_COMMITTED,
                            goap_events::CHANGE_TRANSITIONED
                        ),
                    );
                }
            }
            goap_events::CHANGE_ATTACHED | goap_events::CHANGE_DETACHED => {
                if self.integrators(&src.name).is_empty() {
                    return (
                        false,
                        format!(
                            "no methodology integrates {}: its changes are never attached",
                            src.name
                        ),
                        format!(
                            "list {} in the integrates of the methodology that frames its changes",
                            src.name
                        ),
                    );
                }
            }
            _ => {
                if !standard_event(event) && !src.declares(event) {
                    return (
                        false,
                        format!("{} declares no event {event}", src.name),
                        format!(
                            "declare {event} in the events of {} and emit it (an action's emits), or follow another event",
                            src.name
                        ),
                    );
                }
            }
        }
        (true, String::new(), String::new())
    }

    /// subscription checks one `on` entry of m: its source can emit its event, and the names its filter compares
    /// exist.
    fn subscription(&mut self, path: &str, sub: &Subscription) {
        if !self.m.knows_event(&sub.event) {
            return; // the compiler reports it
        }
        if sub.event == goap_events::METHODOLOGY_PUBLISHED && sub.origin() != FROM_SELF {
            self.add(
                format!("{path}.from"),
                "follow methodology.published with from: self (a trigger), or follow a change event",
                format!(
                    "{} is about no change: it never comes from an {} change",
                    sub.event,
                    sub.origin()
                ),
            );
            return;
        }
        let (srcs, ok) = self.sources(sub);
        if !ok {
            return;
        }
        if srcs.is_empty() {
            if sub.origin() == FROM_INTEGRATOR {
                let name = self.m.name.clone();
                self.add(
                    format!("{path}.from"),
                    format!(
                        "list {name} in the integrates of the methodology that should frame its changes, or follow from: self"
                    ),
                    format!("no methodology integrates {name}: no event ever comes from an integrator"),
                );
            }
            return;
        }
        let (mut whys, mut hints) = (Vec::new(), Vec::new());
        let mut emitting = 0;
        for s in &srcs {
            let (ok, why, hint) = self.emits(s, &sub.event);
            if ok {
                emitting += 1;
            } else {
                whys.push(why);
                hints.push(hint);
            }
        }
        if emitting == 0 {
            self.add(
                format!("{path}.event"),
                hints.join("; "),
                format!(
                    "{} never comes from {}: {}",
                    sub.event,
                    source_label(sub, &srcs),
                    whys.join("; ")
                ),
            );
            return;
        }
        for f in self.filter_findings(sub, &srcs) {
            self.add(format!("{path}.filter"), f.hint, f.msg);
        }
    }

    /// filterFindings are the values a filter compares that no source can produce.
    fn filter_findings(&self, sub: &Subscription, srcs: &[&Methodology]) -> Vec<Finding> {
        let refs = literal_refs(&sub.filter);
        let mut out = Vec::new();
        let (mut names, mut states, mut procs, mut steps, mut paths) =
            (Vec::new(), Vec::new(), Vec::new(), Vec::new(), Vec::new());
        for s in srcs {
            names.push(s.name.clone());
            if let Some(lc) = self.c.lifecycle_of(s) {
                states.extend(state_names(&lc));
            }
            let (p, st, pa) = activity_names(s);
            procs.extend(p);
            steps.extend(st);
            paths.extend(pa);
        }
        let mut check = |sel: &str, what: &str, known: &[String]| {
            for v in refs.get(sel).into_iter().flatten() {
                if v.is_empty() || known.contains(v) {
                    continue;
                }
                out.push(Finding {
                    msg: format!("{sel} {}: no {what} of {}", q(v), source_label(sub, srcs)),
                    hint: suggest(v, known, &format!("{what}s")),
                });
            }
        };
        check("event.change.methodology", "methodology", &names);
        check("event.process.methodology", "methodology", &names);
        if !states.is_empty() {
            check("event.change.state", "state", &states);
        }
        check("event.step.process", "process", &procs);
        check("event.process.agent", "process", &procs);
        check("event.step.name", "step", &steps);
        check("event.step.path", "step path", &paths);
        for v in refs.get("event.type").into_iter().flatten() {
            if *v != sub.event {
                out.push(Finding {
                    msg: format!(
                        "event.type {}: the subscription only receives {}",
                        q(v),
                        sub.event
                    ),
                    hint: format!("compare event.type with {}, or subscribe to {v}", sub.event),
                });
            }
        }
        out
    }

    /// consumers: the other methodologies following the events of m's changes or of its integrators, against what
    /// this version of m can emit.
    fn consumers(&mut self) {
        let m = self.m;
        for n in self.other_names() {
            let o = &self.c.others[&n];
            for ps in o.all_subscriptions() {
                let sub = &ps.sub;
                let follows = match sub.origin() {
                    FROM_INTEGRATED => integrated_names(o).contains(&m.name),
                    FROM_INTEGRATOR => {
                        m.integrates_changes_of(&o.name)
                            && !integrated_names(m).iter().any(|x| x == ANY_METHODOLOGY)
                    }
                    FROM_DEPENDENCY => false,
                    _ => o.applies_to.contains(&m.name),
                };
                if !follows || !o.knows_event(&sub.event) {
                    continue;
                }
                let (ok, why, hint) = self.emits(m, &sub.event);
                if !ok {
                    let path = if sub.event == goap_events::CHANGE_TRANSITIONED {
                        "lifecycle"
                    } else {
                        ""
                    };
                    self.add(
                        path,
                        hint,
                        format!(
                            "{} follows {} {}: {why}",
                            o.name,
                            sub.event,
                            origin_of(sub, &m.name)
                        ),
                    );
                    continue;
                }
                let findings = Check {
                    m: o,
                    c: self.c,
                    out: Vec::new(),
                }
                .filter_findings(sub, &[m]);
                for f in findings {
                    self.add(
                        "",
                        f.hint,
                        format!(
                            "{} follows {} {}, filtered on {}",
                            o.name,
                            sub.event,
                            origin_of(sub, &m.name),
                            f.msg
                        ),
                    );
                }
            }
        }
    }

    // --- guardian -------------------------------------------------------------------------------------------------

    /// guardian checks the guardian of m against the lifecycles of the changes it integrates and its own.
    fn guardian(&mut self) {
        let m = self.m;
        let g = m.guardian.as_ref();
        if g.is_none() && !self.exclusive() {
            return;
        }
        let (framed, wild) = self.below();
        let inn: Vec<&Methodology> = framed.iter().map(|b| b.m).collect();
        let own = self.c.lifecycle_of(m);
        if let Some(g) = g {
            if !g.merge.trim().is_empty() {
                self.guard_refs("guardian.merge", &g.merge, own.as_ref(), &inn, wild);
            }
            for (t, expr) in &g.transitions {
                let path = format!("guardian.transitions.{t}");
                self.guard_refs(&path, expr, own.as_ref(), &inn, wild);
                if wild {
                    continue;
                }
                let mut all = Vec::new();
                let mut with_lc = 0;
                for x in &inn {
                    if let Some(lc) = self.c.lifecycle_of(x) {
                        with_lc += 1;
                        all.extend(transition_names(&lc));
                    }
                }
                if inn.is_empty() {
                } else if with_lc == 0 {
                    self.add(
                        path,
                        "name a transition of their lifecycle, or move the guard to guardian.merge",
                        format!(
                            "the changes {} integrates ({}) follow no lifecycle: this guard is never asked",
                            m.name,
                            names(&inn)
                        ),
                    );
                } else if !all.contains(t) {
                    self.add(
                        path,
                        suggest(t, &all, "transitions"),
                        format!(
                            "no transition {t} in the lifecycle of {}: this guard is never asked",
                            names(&inn)
                        ),
                    );
                }
            }
        }
        if wild {
            return;
        }
        for b in &framed {
            let x = b.m;
            let expr = m.merge_guard(&b.via(x));
            if !expr.is_empty() && self.satisfiable(&expr, x, "") == Sat::Unsat {
                self.add(
                    "guardian.merge",
                    merge_hint(m, &b.via(x)),
                    format!(
                        "the merge guard {expr} never holds for a change of {}: it can never merge",
                        x.name
                    ),
                );
            }
            let Some(g) = g else {
                continue;
            };
            let lc = self.c.lifecycle_of(x);
            for (t, expr) in &g.transitions {
                if !has_transition(lc.as_ref(), t) {
                    continue;
                }
                if self.satisfiable(expr, x, t) == Sat::Unsat {
                    let from = lc.as_ref().map(|lc| from_states(lc, t)).unwrap_or_default();
                    self.add(
                        format!("guardian.transitions.{t}"),
                        format!(
                            "the transition {t} leaves {}: test the state the change is in when it takes it",
                            from.join(", ")
                        ),
                        format!(
                            "the guard of {t} never holds for a change of {}: the transition is never taken",
                            x.name
                        ),
                    );
                }
            }
        }
    }

    fn exclusive(&self) -> bool {
        self.m.integrates.iter().any(|i| i.exclusive)
    }

    /// below are the methodologies m's guardian frames, nearest first; wild when m integrates every methodology.
    fn below(&self) -> (Vec<Framed<'a>>, bool) {
        let (direct, wild) = self.integrated();
        if wild {
            return (Vec::new(), true);
        }
        let mut out = Vec::new();
        let mut seen: HashSet<String> = HashSet::from([self.m.name.clone()]);
        let mut queue: std::collections::VecDeque<Framed<'a>> = std::collections::VecDeque::new();
        for x in direct {
            seen.insert(x.name.clone());
            queue.push_back(Framed {
                m: x,
                between: Vec::new(),
            });
        }
        while let Some(f) = queue.pop_front() {
            if f.between.len() < MAX_COMPOSED_CHAIN {
                for inn in &f.m.integrates {
                    for n in &inn.methodologies {
                        let Some(o) = self.c.others.get(n) else {
                            continue;
                        };
                        if seen.contains(n) {
                            continue;
                        }
                        seen.insert(n.clone());
                        let mut between = vec![f.m];
                        between.extend(f.between.iter().copied());
                        queue.push_back(Framed { m: o, between });
                    }
                }
            }
            out.push(f);
        }
        (out, false)
    }

    /// guardRefs checks the names a guard compares.
    fn guard_refs(
        &mut self,
        path: &str,
        expr: &str,
        own: Option<&Lifecycle>,
        inn: &[&Methodology],
        wild: bool,
    ) {
        let m = self.m;
        let refs = literal_refs(expr);
        if let Some(vs) = refs.get("integrator.state")
            && !vs.is_empty()
        {
            match own {
                None => self.add(
                    path,
                    format!(
                        "give {} a lifecycle (lifecycle: <a lifecycle of its domain>), or test integrator.attached",
                        m.name
                    ),
                    format!("{} names no lifecycle: integrator.state is always empty", m.name),
                ),
                Some(own) => {
                    for v in vs {
                        if !v.is_empty() && !has_state(Some(own), v) {
                            self.add(
                                path,
                                suggest(v, &state_names(own), &format!("states of {}", own.name)),
                                format!(
                                    "integrator.state {}: no state of the lifecycle {} of {}",
                                    q(v),
                                    own.name,
                                    m.name
                                ),
                            );
                        }
                    }
                }
            }
        }
        for v in refs.get("integrator.methodology").into_iter().flatten() {
            if *v != m.name {
                self.add(
                    path,
                    format!(
                        "the integrator a guardian sees is always a change of {}",
                        m.name
                    ),
                    format!(
                        "integrator.methodology {}: the guardian of {} only sees {} changes",
                        q(v),
                        m.name,
                        m.name
                    ),
                );
            }
        }
        if wild || inn.is_empty() {
            return;
        }
        let mut states = Vec::new();
        for x in inn {
            if let Some(lc) = self.c.lifecycle_of(x) {
                states.extend(state_names(&lc));
            }
        }
        for v in refs.get("change.state").into_iter().flatten() {
            if v.is_empty() || states.contains(v) {
                continue;
            }
            if states.is_empty() {
                self.add(
                    path,
                    "test change.status, or give them a lifecycle",
                    format!(
                        "change.state {}: the changes {} integrates ({}) follow no lifecycle",
                        q(v),
                        m.name,
                        names(inn)
                    ),
                );
            } else {
                self.add(
                    path,
                    suggest(v, &states, "states"),
                    format!(
                        "change.state {}: no state of the lifecycle of {}",
                        q(v),
                        names(inn)
                    ),
                );
            }
        }
        let mut conds = Vec::new();
        for x in inn {
            conds.extend(condition_names(x));
        }
        for v in refs.get("world").into_iter().flatten() {
            if !conds.contains(v) {
                self.add(
                    path,
                    suggest(v, &conds, "conditions"),
                    format!(
                        "world.{v}: no condition {v} in {}, the guard reads it as unknown",
                        names(inn)
                    ),
                );
            }
        }
    }

    // --- satisfiability -------------------------------------------------------------------------------------------

    /// satisfiable tries the guard of m's guardian over a change of x (transition "": the merge).
    fn satisfiable(&self, expr: &str, x: &Methodology, transition: &str) -> Sat {
        self.together(
            &[Level {
                expr: expr.to_string(),
                guardian: self.m,
            }],
            x,
            transition,
        )
    }

    /// together reports whether the guards of a chain of guardians hold together on a change of x for some
    /// assignment.
    fn together(&self, levels: &[Level<'_>], x: &Methodology, transition: &str) -> Sat {
        let mut guards: Vec<Guard> = Vec::new();
        let mut world: Vec<String> = Vec::new();
        for l in levels {
            let Ok(g) = goap_guard::compile(&l.expr) else {
                return Sat::Unknown;
            };
            guards.push(g);
            for w in literal_refs(&l.expr).get("world").into_iter().flatten() {
                if !world.contains(w) {
                    world.push(w.clone());
                }
            }
            if world.len() > MAX_WORLD_REFS {
                return Sat::Unknown;
            }
        }
        let mut states = vec![String::new()];
        if let Some(lc) = self.c.lifecycle_of(x) {
            states = if transition.is_empty() {
                state_names(&lc)
            } else {
                from_states(&lc, transition)
            };
        }
        let mut decided = true;
        for st in &states {
            for bits in 0..(1usize << world.len()) {
                let w: BTreeMap<String, bool> = world
                    .iter()
                    .enumerate()
                    .map(|(i, n)| (n.clone(), bits & (1 << i) != 0))
                    .collect();
                let change: Map<String, Value> = json!({"id": "c", "title": "", "intent": "",
                    "status": "active", "methodology": x.name, "state": st, "transition": transition,
                    "decision": "", "goal": "", "parent": "p", "project": "", "data": {}})
                .as_object()
                .cloned()
                .unwrap_or_default();
                let mut all = true;
                for (i, l) in levels.iter().enumerate() {
                    let r = self.some_integrator(&guards[i], l.guardian, &change, &w);
                    if r == Sat::Unknown {
                        decided = false;
                    }
                    if r != Sat::Sat {
                        all = false;
                        break;
                    }
                }
                if all {
                    return Sat::Sat;
                }
            }
        }
        if !decided {
            return Sat::Unknown;
        }
        Sat::Unsat
    }

    /// someIntegrator reports whether the guard holds for some integrator the guardian's change may be.
    fn some_integrator(
        &self,
        g: &Guard,
        guardian: &Methodology,
        change: &Map<String, Value>,
        world: &BTreeMap<String, bool>,
    ) -> Sat {
        let mut cands: Vec<Option<Map<String, Value>>> = vec![None];
        let states = match self.c.lifecycle_of(guardian) {
            Some(lc) => state_names(&lc),
            None => vec![String::new()],
        };
        for st in &states {
            for status in ["draft", "active"] {
                cands.push(
                    json!({"attached": true, "id": "i", "methodology": guardian.name, "state": st,
                        "status": status})
                    .as_object()
                    .cloned(),
                );
            }
        }
        let mut res = Sat::Unsat;
        for inn in cands {
            let f = Facts {
                integrator: inn,
                ..Facts::default()
            };
            match g.check_change(change, &f, world) {
                Err(_) => res = Sat::Unknown,
                Ok(true) => return Sat::Sat,
                Ok(false) => {}
            }
        }
        res
    }

    /// composite checks the guards the chain of guardians adds up on the same change.
    fn composite(&mut self) {
        let m = self.m;
        let above = self.chain_above(m);
        // on m's own changes: the guardians above it
        if above.len() > 1 {
            self.composite_on(m, &above, "", "", "");
        }
        if m.guardian.is_none() && !self.exclusive() {
            return;
        }
        let (framed, _) = self.below();
        for f in &framed {
            let mut levels: Vec<&Methodology> = f.between.clone();
            levels.push(m);
            levels.extend(above.iter().copied());
            if levels.len() < 2 {
                continue;
            }
            self.composite_on(
                f.m,
                &levels,
                &f.via(f.m),
                "guardian.merge",
                "guardian.transitions.",
            );
        }
    }

    /// chainAbove are the methodologies integrating m, then the ones integrating those..., one per level, m left out.
    fn chain_above(&self, m: &Methodology) -> Vec<&'a Methodology> {
        let mut out: Vec<&'a Methodology> = Vec::new();
        let mut seen: HashSet<String> = HashSet::from([m.name.clone()]);
        let mut cur = m.name.clone();
        while out.len() < MAX_COMPOSED_CHAIN {
            let next = self
                .integrators(&cur)
                .into_iter()
                .find(|o| !seen.contains(&o.name));
            let Some(next) = next else {
                break;
            };
            out.push(next);
            seen.insert(next.name.clone());
            cur = next.name.clone();
        }
        out
    }

    /// compositeOn checks the guardians (innermost first) over a change of x, for the merge and each transition of
    /// x's lifecycle more than one of them guards, when m's own guard takes part.
    fn composite_on(
        &mut self,
        x: &Methodology,
        chain: &[&Methodology],
        via: &str,
        merge_path: &str,
        trans_prefix: &str,
    ) {
        let mut merge: Vec<Level<'_>> = Vec::new();
        let mut inner = x.name.clone();
        for g in chain {
            let e = g.merge_guard(&inner);
            if !e.is_empty() {
                merge.push(Level {
                    expr: e,
                    guardian: g,
                });
            }
            inner = g.name.clone();
        }
        let report = |ck: &mut Self, path: &str, what: &str, levels: &[Level<'_>]| {
            let parts: Vec<String> = levels
                .iter()
                .map(|l| format!("{} requires {}", l.guardian.name, l.expr))
                .collect();
            let hint = format!(
                "each holds alone but not together: align the states they require ({})",
                parts.join("; ")
            );
            let chain_names: Vec<&Methodology> = chain.to_vec();
            ck.add(
                path,
                hint,
                format!(
                    "the guardians of {} never hold together on {what} of a change of {}",
                    names(&chain_names),
                    x.name
                ),
            );
        };
        // the guardians above m on its own changes: reported on the methodology
        let own = x.name == self.m.name;
        if merge.len() > 1
            && (own || !self.m.merge_guard(via).is_empty())
            && self.all_alone(&merge, x, "")
            && self.together(&merge, x, "") == Sat::Unsat
        {
            report(self, merge_path, "the merge", &merge);
        }
        let Some(lc) = self.c.lifecycle_of(x) else {
            return;
        };
        for t in transition_names(&lc) {
            let levels: Vec<Level<'_>> = chain
                .iter()
                .filter_map(|g| {
                    let e = g.transition_guard(&t);
                    (!e.is_empty()).then_some(Level {
                        expr: e,
                        guardian: g,
                    })
                })
                .collect();
            if levels.len() > 1
                && (own || !self.m.transition_guard(&t).is_empty())
                && self.all_alone(&levels, x, &t)
                && self.together(&levels, x, &t) == Sat::Unsat
            {
                let path = if trans_prefix.is_empty() {
                    String::new()
                } else {
                    format!("{trans_prefix}{t}")
                };
                report(self, &path, &format!("the transition {t}"), &levels);
            }
        }
    }

    /// allAlone: each guard holds alone.
    fn all_alone(&self, levels: &[Level<'_>], x: &Methodology, transition: &str) -> bool {
        levels.iter().all(|l| {
            self.together(
                &[Level {
                    expr: l.expr.clone(),
                    guardian: l.guardian,
                }],
                x,
                transition,
            ) == Sat::Sat
        })
    }
}

fn source_label(sub: &Subscription, srcs: &[&Methodology]) -> String {
    let n = names(srcs);
    match sub.origin() {
        FROM_INTEGRATED => format!("the integrated changes ({n})"),
        FROM_INTEGRATOR => format!("the integrators ({n})"),
        _ => n,
    }
}

fn origin_of(sub: &Subscription, name: &str) -> String {
    match sub.origin() {
        FROM_INTEGRATED => format!("from the {name} changes it integrates"),
        FROM_INTEGRATOR => format!("from the {name} changes integrating its own"),
        _ => format!("of the {name} changes"),
    }
}

/// integratedNames are the names m lists in its integrations, the wildcard included.
fn integrated_names(m: &Methodology) -> Vec<String> {
    m.integrates
        .iter()
        .flat_map(|i| i.methodologies.iter().cloned())
        .collect()
}

fn merge_hint(m: &Methodology, via: &str) -> String {
    if m.integration_of(via).is_some_and(|i| i.exclusive) {
        return format!(
            "an exclusive integration already requires integrator.attached: the merge guard must hold for an attached change, in a state of {}",
            m.name
        );
    }
    format!(
        "relax the merge guard: some state of {} and of the changes it integrates must satisfy it",
        m.name
    )
}

// --- names ----------------------------------------------------------------------------------------------------------

fn names(ms: &[&Methodology]) -> String {
    ms.iter()
        .map(|m| m.name.as_str())
        .collect::<Vec<_>>()
        .join(", ")
}

fn state_names(lc: &Lifecycle) -> Vec<String> {
    lc.states.iter().map(|s| s.name.clone()).collect()
}

fn has_state(lc: Option<&Lifecycle>, name: &str) -> bool {
    lc.is_some_and(|lc| lc.states.iter().any(|s| s.name == name))
}

fn transition_names(lc: &Lifecycle) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for t in &lc.transitions {
        if !out.contains(&t.name) {
            out.push(t.name.clone());
        }
    }
    out
}

fn has_transition(lc: Option<&Lifecycle>, name: &str) -> bool {
    lc.is_some_and(|lc| transition_names(lc).iter().any(|t| t == name))
}

/// fromStates are the states a transition leaves ("*" or "": every state).
fn from_states(lc: &Lifecycle, name: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for t in &lc.transitions {
        if t.name != name {
            continue;
        }
        if t.from.is_empty() || t.from == "*" {
            return state_names(lc);
        }
        if !out.contains(&t.from) {
            out.push(t.from.clone());
        }
    }
    out
}

/// activityNames are the processes (and methods, which run as agents), the step names and the step paths of m.
fn activity_names(m: &Methodology) -> (Vec<String>, Vec<String>, Vec<String>) {
    let (mut procs, mut steps, mut paths) = (Vec::new(), Vec::new(), Vec::new());
    fn walk(steps: &mut Vec<String>, paths: &mut Vec<String>, prefix: &str, ss: &[Step]) {
        for s in ss {
            steps.push(s.name.clone());
            let p = format!("{prefix}/{}", s.name);
            paths.push(p.clone());
            walk(steps, paths, &p, &s.steps);
        }
    }
    for p in &m.processes {
        procs.push(p.name.clone());
        walk(&mut steps, &mut paths, &p.name, &p.steps);
    }
    for x in &m.methods {
        procs.push(x.name.clone());
        walk(&mut steps, &mut paths, &x.name, &x.steps);
    }
    for a in &m.agents {
        procs.push(a.name.clone());
    }
    (procs, steps, paths)
}

/// conditionNames are the conditions the world of a change of m holds: declared, generated by the compiler and
/// imported from the libraries.
fn condition_names(m: &Methodology) -> Vec<String> {
    let mut out = Vec::new();
    if let (Some(c), _) = m.compile_lenient() {
        out.extend(c.conditions.names());
    }
    out.extend(m.conditions.iter().map(|x| x.name.clone()));
    for lib in &m.imports {
        for d in goap_condition::library(lib).unwrap_or_default() {
            out.push(d.name);
        }
    }
    out
}

const SELECTOR: &str =
    r"((?:event\.)?(?:change|step|process|integrator)\.[A-Za-z_][0-9A-Za-z_]*|event\.type)";
const QUOTED: &str = r#"("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')"#;

static CMP_RE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"(?:^|[^0-9A-Za-z_.]){SELECTOR}\s*(==|!=)\s*{QUOTED}"
    ))
    .expect("regexp")
});
static CMP_REV_RE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"{QUOTED}\s*(==|!=)\s*{SELECTOR}(?:[^0-9A-Za-z_.(]|$)"
    ))
    .expect("regexp")
});
static IN_RE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"(?:^|[^0-9A-Za-z_.]){SELECTOR}\s+in\s+\[([^\]]*)\]"
    ))
    .expect("regexp")
});
static QUOTED_RE: LazyLock<Regex> = LazyLock::new(|| Regex::new(QUOTED).expect("regexp"));
static WORLD_RE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"(?:^|[^0-9A-Za-z_.])world(?:\.([A-Za-z_][0-9A-Za-z_]*)|\[\s*{QUOTED}\s*\])"
    ))
    .expect("regexp")
});

/// literalRefs are the string literals an expression compares with a selector, by selector; "world" holds the
/// names read as world.<name> or world["<name>"].
pub(crate) fn literal_refs(expr: &str) -> HashMap<String, Vec<String>> {
    let mut out: HashMap<String, Vec<String>> = HashMap::new();
    if expr.trim().is_empty() {
        return out;
    }
    let mut add = |sel: &str, v: String| {
        let list = out.entry(sel.to_string()).or_default();
        if !list.contains(&v) {
            list.push(v);
        }
    };
    for g in CMP_RE.captures_iter(expr) {
        add(&g[1], unquote(&g[3]));
    }
    for g in CMP_REV_RE.captures_iter(expr) {
        add(&g[3], unquote(&g[1]));
    }
    for g in IN_RE.captures_iter(expr) {
        for qm in QUOTED_RE.find_iter(&g[2]) {
            add(&g[1], unquote(qm.as_str()));
        }
    }
    for g in WORLD_RE.captures_iter(expr) {
        match g.get(1) {
            Some(n) if !n.as_str().is_empty() => add("world", n.as_str().to_string()),
            _ => add("world", unquote(g.get(2).map(|m| m.as_str()).unwrap_or(""))),
        }
    }
    out
}

fn unquote(s: &str) -> String {
    if s.len() >= 2 {
        s[1..s.len() - 1].to_string()
    } else {
        s.to_string()
    }
}

/// suggest is the hint for a name not found among known: the closest one, else the list (what: what they are).
pub(crate) fn suggest(v: &str, known: &[String], what: &str) -> String {
    if known.is_empty() {
        return format!("there are no {what}");
    }
    let mut uniq: Vec<String> = known.to_vec();
    uniq.sort();
    uniq.dedup();
    let (mut best, mut best_d) = (String::new(), v.len() / 2 + 2);
    for k in &uniq {
        let d = levenshtein(&v.to_lowercase(), &k.to_lowercase());
        if d < best_d {
            best = k.clone();
            best_d = d;
        }
    }
    let mut list = uniq;
    if list.len() > 12 {
        list.truncate(12);
        list.push("…".into());
    }
    if !best.is_empty() {
        return format!("did you mean {}? ({what}: {})", q(&best), list.join(", "));
    }
    format!("{what}: {}", list.join(", "))
}

pub(crate) fn levenshtein(a: &str, b: &str) -> usize {
    let (ra, rb): (Vec<char>, Vec<char>) = (a.chars().collect(), b.chars().collect());
    let mut prev: Vec<usize> = (0..=rb.len()).collect();
    for i in 1..=ra.len() {
        let mut cur = vec![0; rb.len() + 1];
        cur[0] = i;
        for j in 1..=rb.len() {
            let cost = usize::from(ra[i - 1] != rb[j - 1]);
            cur[j] = (prev[j] + 1).min(cur[j - 1] + 1).min(prev[j - 1] + cost);
        }
        prev = cur;
    }
    prev[rb.len()]
}
