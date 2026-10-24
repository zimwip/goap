//! A read query in the subset of openCypher (ADR 0160) over a view (Go's `graph/graph/query.go`): MATCH and OPTIONAL
//! MATCH patterns, WHERE, EXISTS, RETURN. A pattern starts from an anchor (a variable bound already, a key or an id,
//! else the nodes of a label, read a page at a time) and grows a hop at a time through the neighbourhoods of the view,
//! each reading the link types its relationship names. Nothing reads a namespace whole: the anchors, the hops and the
//! rows are capped.
//!
//! The match is Go's depth first search, in the same order (so the same reads, the same caps, the same rows): its
//! continuations are frames of a list ([`Goal`]), the bindings, the links used and the trails of the patterns stacks
//! of the run.
use std::any::Any;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use cypher::ast::{Direction, Exists, Expr, NodePattern, Op, Pattern, Query, RelPattern, Return, conjuncts};
use cypher::eval::{self, Entity, EvalError, EvalFuture, MapScope, Path, Scope, Value};
use serde_json::Value as Json;

use crate::error::{GraphError, Kind, Result};
use crate::model::{BaselineId, Link, LinkId, Node, NodeId, NodeQuery, NodeRef};
use crate::read::World;
use crate::store::ReadTx;

/// The most rows a match binds (before RETURN groups them).
pub const QUERY_MAX_ROWS: usize = 2000;
/// The most reads a query makes: the neighbourhoods of its hops and the pages of its anchors.
pub const QUERY_MAX_READS: usize = 2000;
/// The most nodes a label anchor reads.
pub const QUERY_MAX_ANCHORS: usize = 500;
/// The most hops of a variable length relationship (`*`: 1 to QUERY_MAX_DEPTH).
pub const QUERY_MAX_DEPTH: usize = 6;
/// The most times a query runs to learn what its caller reads.
const QUERY_MAX_RUNS: usize = 4;

/// The variables bound before the match and the caps of a query.
#[derive(Debug, Clone)]
pub struct QueryOptions {
    pub bind: BTreeMap<String, NodeId>,
    pub max_rows: usize,
    pub max_reads: usize,
    pub max_anchors: usize,
}

impl Default for QueryOptions {
    fn default() -> Self {
        QueryOptions {
            bind: BTreeMap::new(),
            max_rows: QUERY_MAX_ROWS,
            max_reads: QUERY_MAX_READS,
            max_anchors: QUERY_MAX_ANCHORS,
        }
    }
}

/// What a query answers: its columns and rows (a node, a link and a path are objects naming them), the nodes and
/// links the rows name, whether a cap stopped it and which.
#[derive(Debug, Clone, Default)]
pub struct QueryResult {
    pub columns: Vec<String>,
    pub rows: Vec<Vec<Json>>,
    pub nodes: Vec<Node>,
    pub links: Vec<Link>,
    pub truncated: bool,
    pub notes: Vec<String>,
    pub reads: usize,
    pub baseline: BaselineId,
}

/// A predicate over the node types: whether a type is a type or one of its subtypes.
pub type IsA<'f> = &'f (dyn Fn(&str, &str) -> bool + Sync);
/// What the caller reads of the nodes met, asked after a run: the nodes it does not read are left out of the next.
pub type Visible<'f> = &'f (dyn Fn(&Node) -> bool + Sync);

fn invalid(msg: impl std::fmt::Display) -> GraphError {
    GraphError::new(Kind::Invalid, format!("invalid: {msg}"))
}

/// Runs a read query in a view. A query the Rust parser does not read is left to the Go service (unimplemented); a
/// pattern with no anchor is invalid.
pub async fn query_view<T: ReadTx>(
    w: &World<'_, T>,
    is_a: IsA<'_>,
    src: &str,
    opts: &QueryOptions,
    visible: Option<Visible<'_>>,
) -> Result<QueryResult> {
    let Some(q) = cypher::parse(src) else {
        return Err(GraphError::unimplemented("a query the Rust parser does not read"));
    };
    let plan = plan(q, &opts.bind, visible.is_some()).map_err(invalid)?;
    // what the caller reads, learnt run after run
    let mut vis: Option<HashMap<NodeId, bool>> = visible.map(|_| HashMap::new());
    let mut reads = 0;
    let mut attempt = 0;
    loop {
        attempt += 1;
        // the first run sees every node, to learn which ones the caller reads
        let ran_vis = attempt > 1 && vis.is_some();
        let mut run = Run::new(w, is_a, &plan, opts, if ran_vis { vis.as_ref() } else { None });
        let r = run.matches().await;
        reads += run.reads;
        match r {
            Ok(()) | Err(Flow::Stop) | Err(Flow::Found) => {}
            Err(Flow::Graph(e)) => return Err(e),
            Err(Flow::Eval(m)) => return Err(invalid(m)),
        }
        let Run {
            nodes,
            pending,
            rows,
            mut truncated,
            mut notes,
            links,
            ..
        } = run;
        if let (Some(visible), Some(vis)) = (visible, vis.as_mut()) {
            let met: Vec<&Node> = if ran_vis {
                pending.values().collect()
            } else {
                nodes.values().map(|q| &q.0).collect()
            };
            let mut hidden = false;
            for n in met {
                let ok = visible(n);
                vis.insert(n.id.clone(), ok);
                hidden = hidden || !ok || ran_vis; // a node met for the first time was left out of this run
            }
            if hidden && attempt < QUERY_MAX_RUNS {
                continue;
            }
            if hidden && !pending.is_empty() {
                truncated = true;
                note(
                    &mut notes,
                    "some nodes met late were left out: anchor the query more narrowly".into(),
                );
            }
        }
        let (columns, values) = cypher::project::project(&plan.ret, &plan.vars, &rows).map_err(|e| invalid(e.0))?;
        let mut out = Exported::default();
        let rows = values
            .iter()
            .map(|row| row.iter().map(|v| out.export(v)).collect())
            .collect();
        let (nodes, links) = out.finish(&nodes, &links);
        return Ok(QueryResult {
            columns,
            rows,
            nodes,
            links,
            truncated,
            notes,
            reads,
            baseline: w.baseline.id.clone(),
        });
    }
}

fn note(notes: &mut Vec<String>, n: String) {
    if !notes.contains(&n) {
        notes.push(n);
    }
}

/// A query made ready to match: its clauses, the conditions each checks, the EXISTS of its RETURN.
struct Plan {
    ret: Return,
    clauses: Vec<Clause>,
    /// the conditions of the MATCH clauses, checked again on each row
    all: Vec<Cond>,
    /// the variables of the patterns, in order: RETURN *
    vars: Vec<String>,
    late: Vec<Arc<Exists>>,
    /// the rows to bind before stopping (None: all)
    stream: Option<usize>,
}

/// A MATCH or an OPTIONAL MATCH: its patterns, the conditions checked while it matches (those of the MATCH clauses up
/// to it, and its own WHERE for an OPTIONAL MATCH) and the variables it binds.
struct Clause {
    patterns: Arc<Vec<Pattern>>,
    optional: bool,
    own: Vec<Cond>,
    conds: Arc<Vec<Cond>>,
    binds: Vec<String>,
}

/// A condition with the variables it reads, checked as soon as they are bound.
#[derive(Clone)]
struct Cond {
    expr: Expr,
    vars: Vec<String>,
}

fn exists_var(i: usize) -> String {
    format!("$exists{i}")
}

/// Splits the clauses and their WHERE, checks the variables they read and takes the EXISTS out of RETURN.
fn plan(q: Query, bind: &BTreeMap<String, NodeId>, visible: bool) -> Result<Plan, String> {
    let mut kinds: HashMap<String, &'static str> = HashMap::new();
    let mut vars: Vec<String> = Vec::new();
    let mut declare = |kinds: &mut HashMap<String, &'static str>, name: &str, kind: &'static str| {
        if name.is_empty() {
            return Ok(());
        }
        if let Some(k) = kinds.get(name) {
            if *k != kind || kind != "node" {
                return Err(format!("variable {name} is bound twice"));
            }
            return Ok(());
        }
        kinds.insert(name.to_string(), kind);
        vars.push(name.to_string());
        Ok(())
    };
    for name in bind.keys() {
        declare(&mut kinds, name, "node")?;
    }
    // each conjunct of a WHERE with the variables of the query it reads (those an EXISTS binds itself apart)
    let conds = |kinds: &HashMap<String, &'static str>, filter: Option<&Expr>| -> Result<Vec<Cond>, String> {
        let mut out = Vec::new();
        for c in conjuncts(filter) {
            defined(c, kinds)?;
            let vars = c.vars().into_iter().filter(|v| kinds.contains_key(v)).collect();
            out.push(Cond { expr: c.clone(), vars });
        }
        Ok(out)
    };
    let mut clauses: Vec<Clause> = Vec::new();
    for m in &q.matches {
        let mut binds: Vec<String> = Vec::new();
        for pt in m.patterns.iter() {
            for (i, n) in pt.nodes.iter().enumerate() {
                if !n.var.is_empty() && !kinds.contains_key(&n.var) {
                    binds.push(n.var.clone());
                }
                declare(&mut kinds, &n.var, "node")?;
                if let Some(r) = pt.rels.get(i) {
                    if !r.var.is_empty() && !binds.contains(&r.var) {
                        binds.push(r.var.clone());
                    }
                    declare(&mut kinds, &r.var, "relationship")?;
                }
            }
            if !pt.var.is_empty() {
                binds.push(pt.var.clone());
            }
            declare(&mut kinds, &pt.var, "path")?;
        }
        // an OPTIONAL MATCH's WHERE reads what is bound by it or before it
        let own = if m.optional {
            conds(&kinds, m.filter.as_ref())?
        } else {
            Vec::new()
        };
        clauses.push(Clause {
            patterns: m.patterns.clone(),
            optional: m.optional,
            own,
            conds: Arc::new(Vec::new()),
            binds,
        });
    }
    // a MATCH's WHERE may read any variable of the query
    let mut all = Vec::new();
    for (i, m) in q.matches.iter().enumerate() {
        if m.optional {
            continue;
        }
        let cs = conds(&kinds, m.filter.as_ref())?;
        all.extend(cs.iter().cloned());
        clauses[i].own = cs;
    }
    let mut up_to: Vec<Cond> = Vec::new();
    for cl in &mut clauses {
        if !cl.optional {
            up_to.extend(cl.own.iter().cloned());
            cl.conds = Arc::new(up_to.clone());
        } else {
            let mut cs = up_to.clone();
            cs.extend(cl.own.iter().cloned());
            cl.conds = Arc::new(cs);
        }
    }
    // the EXISTS of RETURN and ORDER BY are matched with the row, their values variables of it
    let mut late: Vec<Arc<Exists>> = Vec::new();
    let mut lift = |e: Expr| {
        e.rewrite(&mut |x| match x {
            Expr::Exists(ex) => {
                late.push(ex);
                Expr::Var(exists_var(late.len() - 1))
            }
            x => x,
        })
    };
    let mut ret = q.ret;
    let mut aliases: HashSet<String> = HashSet::new();
    for it in &mut ret.items {
        defined(&it.expr, &kinds)?;
        it.expr = lift(std::mem::replace(&mut it.expr, Expr::Lit(Value::Null)));
        aliases.insert(it.name.clone());
    }
    for s in &mut ret.order {
        let declared = s.expr.declared();
        for v in s.expr.vars() {
            if !kinds.contains_key(&v) && !aliases.contains(&v) && !declared.contains(&v) {
                return Err(format!("variable {v} is not defined"));
            }
        }
        s.expr = lift(std::mem::replace(&mut s.expr, Expr::Lit(Value::Null)));
    }
    cypher::project::bounds(&ret).map_err(|e| e.0)?;
    // rows a hidden node leaves out are found by the next run: the first one goes on to its caps
    let stream = match cypher::project::streams(&ret) {
        Some(n) if !visible => n,
        _ => None,
    };
    Ok(Plan {
        ret,
        clauses,
        all,
        vars,
        late,
        stream,
    })
}

/// Refuses an expression reading a variable no pattern binds (the patterns of its EXISTS included).
fn defined(e: &Expr, kinds: &HashMap<String, &'static str>) -> Result<(), String> {
    let local = e.declared();
    for v in e.vars() {
        if !kinds.contains_key(&v) && !local.contains(&v) {
            return Err(format!("variable {v} is not defined"));
        }
    }
    Ok(())
}

/// How a match ends early: a cap or the rows a streaming RETURN keeps (Stop), the first witness of an EXISTS (Found),
/// a failure.
enum Flow {
    Stop,
    Found,
    Graph(GraphError),
    Eval(String),
}

impl From<EvalError> for Flow {
    fn from(e: EvalError) -> Self {
        Flow::Eval(e.0)
    }
}

impl From<GraphError> for Flow {
    fn from(e: GraphError) -> Self {
        Flow::Graph(e)
    }
}

type Fut<'f, T> = Pin<Box<dyn Future<Output = Result<T, Flow>> + Send + 'f>>;

/// What the match does next: a list of goals.
type K = Option<Arc<Frame>>;

struct Frame {
    goal: Goal,
    next: K,
}

fn frame(goal: Goal, next: K) -> K {
    Some(Arc::new(Frame { goal, next }))
}

enum Goal {
    /// the patterns of clause `i` matched: an OPTIONAL MATCH checks its own WHERE, then the next clause
    Clause {
        i: usize,
        found: usize,
        saved: Arc<Vec<Cond>>,
    },
    /// the patterns of an EXISTS matched: its WHERE, then found
    Exists { conds: Arc<Vec<Cond>> },
    /// the patterns from `i` on
    Patterns { ps: Arc<Vec<Pattern>>, i: usize },
    /// pattern `pi` extended from its node `k` to its last one, the trail `tr`
    Right {
        ps: Arc<Vec<Pattern>>,
        pi: usize,
        k: usize,
        tr: usize,
    },
    /// pattern `pi` extended from its node `k` to its first one
    Left {
        ps: Arc<Vec<Pattern>>,
        pi: usize,
        k: usize,
        tr: usize,
    },
    /// the path variable of pattern `pi`, then the next pattern
    PathVar {
        ps: Arc<Vec<Pattern>>,
        pi: usize,
        tr: usize,
    },
}

/// A relationship pattern followed from the trail's node `k`: to the right (along the pattern) or to the left.
struct Side {
    ps: Arc<Vec<Pattern>>,
    pi: usize,
    k: usize,
    tr: usize,
    right: bool,
    next: K,
}

impl Side {
    fn rel(&self) -> &RelPattern {
        &self.ps[self.pi].rels[if self.right { self.k } else { self.k - 1 }]
    }

    fn target(&self) -> &NodePattern {
        &self.ps[self.pi].nodes[if self.right { self.k + 1 } else { self.k - 1 }]
    }

    fn dir(&self) -> Direction {
        if self.right {
            self.rel().dir
        } else {
            self.rel().dir.reversed()
        }
    }
}

/// What a pattern bound so far: its nodes, the links of each relationship (several for a variable length one, from
/// its left node to its right one) and the nodes between them.
struct Trail {
    nodes: Vec<Option<Arc<QNode>>>,
    rels: Vec<Vec<Link>>,
    mids: Vec<Vec<Arc<QNode>>>,
}

impl Trail {
    fn path(&self) -> Path {
        let mut p = Path::default();
        for (i, n) in self.nodes.iter().enumerate() {
            if let Some(n) = n {
                p.nodes.push(n.clone());
            }
            if i < self.rels.len() {
                for (j, l) in self.rels[i].iter().enumerate() {
                    p.rels.push(Arc::new(QLink(l.clone())));
                    if let Some(m) = self.mids[i].get(j) {
                        p.nodes.push(m.clone());
                    }
                }
            }
        }
        p
    }
}

/// A link of a node in the view, with the node at its other end.
struct Hop {
    link: Link,
    other: Arc<QNode>,
    /// the link leaves the node
    out: bool,
    /// the link enters the node (both for a link to itself)
    into: bool,
}

/// The bindings a node or a relationship made: none to take back, or its variable.
type Undo = Option<String>;

struct Run<'r, 'w, T> {
    w: &'r World<'w, T>,
    is_a: IsA<'r>,
    plan: &'r Plan,
    opts: &'r QueryOptions,
    /// the conditions a binding checks: those of the clause, or of the EXISTS, being matched
    conds: Arc<Vec<Cond>>,
    /// what the caller reads (None: every node)
    vis: Option<&'r HashMap<NodeId, bool>>,
    /// the nodes met that vis does not know yet, left out of this run
    pending: HashMap<NodeId, Node>,
    nodes: HashMap<NodeId, Arc<QNode>>,
    hoods: HashMap<String, Arc<Vec<Hop>>>,
    links: HashMap<LinkId, Link>,
    reads: usize,
    rows: Vec<MapScope>,
    truncated: bool,
    notes: Vec<String>,
    /// the bindings (an EXISTS matches over a copy of the row), the links a clause used, the trails of the patterns
    /// and whether each clause matched: stacks, the top the innermost
    scopes: Vec<MapScope>,
    used: Vec<HashSet<LinkId>>,
    trails: Vec<Trail>,
    found: Vec<bool>,
}

impl<'r, 'w, T: ReadTx> Run<'r, 'w, T> {
    fn new(
        w: &'r World<'w, T>,
        is_a: IsA<'r>,
        plan: &'r Plan,
        opts: &'r QueryOptions,
        vis: Option<&'r HashMap<NodeId, bool>>,
    ) -> Self {
        Run {
            w,
            is_a,
            plan,
            opts,
            conds: Arc::new(Vec::new()),
            vis,
            pending: HashMap::new(),
            nodes: HashMap::new(),
            hoods: HashMap::new(),
            links: HashMap::new(),
            reads: 0,
            rows: Vec::new(),
            truncated: false,
            notes: Vec::new(),
            scopes: vec![MapScope::new()],
            used: Vec::new(),
            trails: Vec::new(),
            found: Vec::new(),
        }
    }

    fn top(&self) -> &MapScope {
        self.scopes.last().expect("a scope")
    }

    fn top_mut(&mut self) -> &mut MapScope {
        self.scopes.last_mut().expect("a scope")
    }

    fn undo(&mut self, u: Undo) {
        if let Some(name) = u {
            self.top_mut().remove(&name);
        }
    }

    /// Ends the match on a cap.
    fn stop(&mut self, msg: String) -> Flow {
        self.truncated = true;
        note(&mut self.notes, msg);
        Flow::Stop
    }

    /// Counts a read against the cap.
    fn read(&mut self) -> Result<(), Flow> {
        if self.reads >= self.opts.max_reads {
            let msg = format!(
                "stopped after {} reads: anchor the query more narrowly",
                self.opts.max_reads
            );
            return Err(self.stop(msg));
        }
        self.reads += 1;
        Ok(())
    }

    async fn matches(&mut self) -> Result<(), Flow> {
        for (name, id) in &self.opts.bind {
            let ns = self.by_id(id).await?;
            let Some(n) = ns.into_iter().next() else {
                return Err(GraphError::not_found(format!("node {id} is not in the view")).into());
            };
            self.top_mut().insert(name.clone(), Value::Entity(n));
        }
        self.clause(0).await
    }

    /// Matches the clauses from i on, then binds a row; an OPTIONAL MATCH that matches nothing binds its variables to
    /// null.
    fn clause(&mut self, i: usize) -> Fut<'_, ()> {
        Box::pin(async move {
            let plan = self.plan;
            if i == plan.clauses.len() {
                return self.emit().await;
            }
            let cl = &plan.clauses[i];
            let saved = std::mem::replace(&mut self.conds, cl.conds.clone());
            let found = self.found.len();
            self.found.push(false);
            self.used.push(HashSet::new());
            let k = frame(
                Goal::Patterns {
                    ps: cl.patterns.clone(),
                    i: 0,
                },
                frame(
                    Goal::Clause {
                        i,
                        found,
                        saved: saved.clone(),
                    },
                    None,
                ),
            );
            let r = self.solve(k).await;
            self.used.pop();
            let matched = self.found.pop() == Some(true);
            if r.is_err() || matched || !cl.optional {
                self.conds = saved;
                return r;
            }
            self.conds = Arc::new(Vec::new()); // the nulls are checked with the row (emit)
            let mut bound = Vec::new();
            for v in &cl.binds {
                if self.top().contains_key(v) {
                    continue;
                }
                self.top_mut().insert(v.clone(), Value::Null);
                bound.push(v.clone());
            }
            let r = self.clause(i + 1).await;
            for v in bound {
                self.top_mut().remove(&v);
            }
            self.conds = saved;
            r
        })
    }

    /// Binds a row: the conditions once more (the ones no binding checked), the EXISTS of RETURN.
    async fn emit(&mut self) -> Result<(), Flow> {
        let plan = self.plan;
        for c in &plan.all {
            if !self.holds(&c.expr).await? {
                return Ok(());
            }
        }
        if self.rows.len() >= self.opts.max_rows {
            let msg = format!("stopped after {} rows", self.opts.max_rows);
            return Err(self.stop(msg));
        }
        let b = self.top().clone();
        let mut row = b.clone();
        for (i, ex) in plan.late.iter().enumerate() {
            let ok = self.exists(ex, b.clone()).await?;
            row.insert(exists_var(i), Value::Bool(ok));
        }
        self.rows.push(row);
        if let Some(n) = plan.stream
            && self.rows.len() >= n
        {
            return Err(Flow::Stop);
        }
        Ok(())
    }

    /// Matches the patterns of an EXISTS in a row: true at the first witness. The variables the row binds are those of
    /// the row, the others local to it.
    fn exists<'f>(&'f mut self, ex: &'f Exists, b: MapScope) -> Fut<'f, bool> {
        Box::pin(async move {
            let conds: Vec<Cond> = conjuncts(ex.filter.as_ref())
                .into_iter()
                .map(|c| Cond {
                    expr: c.clone(),
                    vars: c.vars(),
                })
                .collect();
            let conds = Arc::new(conds);
            let saved = std::mem::replace(&mut self.conds, conds.clone());
            self.scopes.push(b);
            self.used.push(HashSet::new());
            let k = frame(
                Goal::Patterns {
                    ps: ex.patterns.clone(),
                    i: 0,
                },
                frame(Goal::Exists { conds }, None),
            );
            let r = self.solve(k).await;
            self.used.pop();
            self.scopes.pop();
            self.conds = saved;
            match r {
                Err(Flow::Found) => Ok(true),
                Ok(()) => Ok(false),
                Err(e) => Err(e),
            }
        })
    }

    /// Goes on with what is next.
    fn solve(&mut self, k: K) -> Fut<'_, ()> {
        Box::pin(async move {
            let Some(f) = k else { return Ok(()) };
            match &f.goal {
                Goal::Clause { i, found, saved } => {
                    let plan = self.plan;
                    let cl = &plan.clauses[*i];
                    if cl.optional {
                        // the ones no binding checked (read before the clause, or constants)
                        for c in &cl.own {
                            if !self.holds(&c.expr).await? {
                                return Ok(());
                            }
                        }
                    }
                    self.found[*found] = true;
                    let inner = std::mem::replace(&mut self.conds, saved.clone());
                    let r = self.clause(i + 1).await;
                    self.conds = inner;
                    r
                }
                Goal::Exists { conds } => {
                    for c in conds.iter() {
                        if !self.holds(&c.expr).await? {
                            return Ok(());
                        }
                    }
                    Err(Flow::Found)
                }
                Goal::Patterns { ps, i } => self.patterns(ps.clone(), *i, f.next.clone()).await,
                Goal::Right { ps, pi, k, tr } => {
                    let p = &ps[*pi];
                    if *k == p.nodes.len() - 1 {
                        return self.solve(f.next.clone()).await;
                    }
                    let side = Side {
                        ps: ps.clone(),
                        pi: *pi,
                        k: *k,
                        tr: *tr,
                        right: true,
                        next: f.next.clone(),
                    };
                    self.step(&side).await
                }
                Goal::Left { ps, pi, k, tr } => {
                    if *k == 0 {
                        return self.solve(f.next.clone()).await;
                    }
                    let side = Side {
                        ps: ps.clone(),
                        pi: *pi,
                        k: *k,
                        tr: *tr,
                        right: false,
                        next: f.next.clone(),
                    };
                    self.step(&side).await
                }
                Goal::PathVar { ps, pi, tr } => {
                    let var = &ps[*pi].var;
                    let next = frame(
                        Goal::Patterns {
                            ps: ps.clone(),
                            i: pi + 1,
                        },
                        f.next.clone(),
                    );
                    if var.is_empty() {
                        return self.solve(next).await;
                    }
                    let path = Value::Path(self.trails[*tr].path());
                    let Some(u) = self.bind(var, path).await? else {
                        return Ok(());
                    };
                    let r = self.solve(next).await;
                    self.undo(u);
                    r
                }
            }
        })
    }

    /// Matches the patterns from i on, then goes on with next.
    fn patterns(&mut self, ps: Arc<Vec<Pattern>>, i: usize, next: K) -> Fut<'_, ()> {
        Box::pin(async move {
            if i == ps.len() {
                return self.solve(next).await;
            }
            let p = &ps[i];
            let (at, cands) = self.anchor(p).await?;
            let tr = self.trails.len();
            self.trails.push(Trail {
                nodes: vec![None; p.nodes.len()],
                rels: vec![Vec::new(); p.rels.len()],
                mids: vec![Vec::new(); p.rels.len()],
            });
            let mut res = Ok(());
            for n in cands {
                let u = match self.bind_node(&p.nodes[at], &n).await {
                    Ok(Some(u)) => u,
                    Ok(None) => continue,
                    Err(e) => {
                        res = Err(e);
                        break;
                    }
                };
                self.trails[tr].nodes[at] = Some(n);
                let k = frame(
                    Goal::Right {
                        ps: ps.clone(),
                        pi: i,
                        k: at,
                        tr,
                    },
                    frame(
                        Goal::Left {
                            ps: ps.clone(),
                            pi: i,
                            k: at,
                            tr,
                        },
                        frame(
                            Goal::PathVar {
                                ps: ps.clone(),
                                pi: i,
                                tr,
                            },
                            next.clone(),
                        ),
                    ),
                );
                let r = self.solve(k).await;
                self.undo(u);
                if let Err(e) = r {
                    res = Err(e);
                    break;
                }
            }
            self.trails.pop();
            res
        })
    }

    /// The node pattern a pattern starts from and its candidate nodes: a variable already bound, else a key or an id,
    /// else the nodes of a label (narrowed by the text of a WHERE on it).
    async fn anchor(&mut self, p: &Pattern) -> Result<(usize, Vec<Arc<QNode>>), Flow> {
        for (i, n) in p.nodes.iter().enumerate() {
            if n.var.is_empty() {
                continue;
            }
            if let Some(v) = self.top().get(&n.var) {
                // null (an OPTIONAL MATCH that matched nothing): no node
                let q = match v {
                    Value::Entity(e) => e.as_any().downcast_ref::<QNode>().and_then(|q| self.nodes.get(&q.0.id)),
                    _ => None,
                };
                return Ok((i, q.cloned().into_iter().collect()));
            }
        }
        for (i, n) in p.nodes.iter().enumerate() {
            if let Some(key) = self.literal(n, "key") {
                return Ok((i, self.by_key(&key).await?));
            }
            if let Some(id) = self.literal(n, "id") {
                return Ok((i, self.by_id(&NodeId(id)).await?));
            }
        }
        for (i, n) in p.nodes.iter().enumerate() {
            if !n.labels.is_empty() {
                return Ok((i, self.of_label(n).await?));
            }
        }
        Err(invalid(
            "a pattern starts from a node with a label, a key or an id, or a variable an earlier pattern bound: \
             (n:Requirement), (n {key: 'R-1'})",
        )
        .into())
    }

    /// A string a node pattern sets a field to: inline (`{key: 'x'}`) or by an equality of the WHERE (`n.key = 'x'`).
    fn literal(&self, n: &NodePattern, field: &str) -> Option<String> {
        if let Some((_, e)) = n.props.iter().rev().find(|(k, _)| k == field)
            && let Expr::Lit(l) = e
        {
            return match l {
                Value::Str(s) => Some(s.clone()),
                _ => None,
            };
        }
        if n.var.is_empty() {
            return None;
        }
        for c in self.conds.iter() {
            let Expr::Binary(Op::Eq, l, r) = &c.expr else { continue };
            for (a, b) in [(l, r), (r, l)] {
                if let (Expr::Prop(x, key), Expr::Lit(Value::Str(s))) = (a.as_ref(), b.as_ref())
                    && key == field
                    && matches!(x.as_ref(), Expr::Var(v) if *v == n.var)
                {
                    return Some(s.clone());
                }
            }
        }
        None
    }

    /// The longest string a WHERE asks a property of a node pattern to equal, contain, start or end with: the text
    /// filter of its label's pages (a superset of what the condition keeps, which is checked once bound).
    fn text(&self, n: &NodePattern) -> String {
        let mut best = String::new();
        let mut consider = |s: &str| {
            if s.len() > best.len() {
                best = s.to_string();
            }
        };
        for (k, e) in &n.props {
            if let Expr::Lit(Value::Str(s)) = e
                && k != "type"
            {
                consider(s);
            }
        }
        if n.var.is_empty() {
            return best;
        }
        for c in self.conds.iter() {
            let Expr::Binary(op, l, r) = &c.expr else { continue };
            if !matches!(op, Op::Eq | Op::Contains | Op::StartsWith | Op::EndsWith) {
                continue;
            }
            let (pr, lit) = match (l.as_ref(), r.as_ref()) {
                (p @ Expr::Prop(..), Expr::Lit(v)) => (p, v),
                (Expr::Lit(v), p @ Expr::Prop(..)) if *op == Op::Eq => (p, v),
                _ => continue,
            };
            if let Expr::Prop(x, key) = pr
                && matches!(x.as_ref(), Expr::Var(v) if *v == n.var)
                && key != "type"
                && let Value::Str(s) = lit
            {
                consider(s);
            }
        }
        best
    }

    async fn by_key(&mut self, key: &str) -> Result<Vec<Arc<QNode>>, Flow> {
        self.read()?;
        let r = match self.w.find(&NodeId::default(), key).await {
            Ok(r) => r,
            Err(e) if e.is_not_found() => return Ok(Vec::new()),
            Err(e) => return Err(e.into()),
        };
        self.node_at(&r).await
    }

    async fn by_id(&mut self, id: &NodeId) -> Result<Vec<Arc<QNode>>, Flow> {
        self.read()?;
        let at = self.w.at(std::slice::from_ref(id)).await?;
        let Some(&v) = at.get(id) else {
            return Ok(Vec::new());
        };
        self.node_at(&NodeRef::new(id.clone(), v)).await
    }

    async fn node_at(&mut self, r: &NodeRef) -> Result<Vec<Arc<QNode>>, Flow> {
        if let Some(n) = self.nodes.get(&r.id) {
            return Ok(vec![n.clone()]);
        }
        let n = self.w.node(r).await?;
        if !self.w.sees(&n) {
            return Ok(Vec::new());
        }
        Ok(self.keep(n).into_iter().collect())
    }

    /// Notes a node read, none for a node the caller does not read (or not known yet: the next run asks for it).
    fn keep(&mut self, n: Node) -> Option<Arc<QNode>> {
        if let Some(q) = self.nodes.get(&n.id) {
            return Some(q.clone());
        }
        if let Some(vis) = self.vis {
            match vis.get(&n.id) {
                Some(true) => {}
                Some(false) => return None,
                None => {
                    self.pending.insert(n.id.clone(), n);
                    return None;
                }
            }
        }
        let q = Arc::new(QNode(n));
        self.nodes.insert(q.0.id.clone(), q.clone());
        Some(q)
    }

    /// The nodes of the first label of a node pattern, a page of at most max_anchors: the type as written, and a label
    /// naming no namespace in the namespace of the view.
    async fn of_label(&mut self, n: &NodePattern) -> Result<Vec<Arc<QNode>>, Flow> {
        let label = &n.labels[0];
        let mut types = vec![label.clone()];
        if !label.contains('@') {
            types.push(format!("{}@{label}", self.w.namespace));
        }
        let text = self.text(n);
        let max = self.opts.max_anchors;
        let mut out = Vec::new();
        for typ in types {
            self.read()?;
            let room = max - out.len();
            let page = self
                .w
                .page(&NodeQuery {
                    typ: typ.clone(),
                    text: text.clone(),
                    limit: room + 1,
                    ..NodeQuery::default()
                })
                .await?;
            for node in page.nodes {
                if node.typ != typ {
                    continue;
                }
                if out.len() == max {
                    self.truncated = true;
                    let msg = format!(
                        "only the first {max} nodes of {label} were read (of {}): narrow the anchor with a key or a \
                         WHERE on a property",
                        page.total
                    );
                    note(&mut self.notes, msg);
                    break;
                }
                if let Some(q) = self.keep(node) {
                    out.push(q);
                }
            }
        }
        Ok(out)
    }

    /// Binds a node to a node pattern when it matches: its labels, its properties, the variable (the same node when
    /// bound already) and the conditions it completes. None: no match.
    async fn bind_node(&mut self, p: &NodePattern, n: &Arc<QNode>) -> Result<Option<Undo>, Flow> {
        for l in &p.labels {
            if !self.is_label(&n.0.typ, l) {
                return Ok(None);
            }
        }
        if !self.props_match(&p.props, n.as_ref())? {
            return Ok(None);
        }
        if p.var.is_empty() {
            return Ok(Some(None));
        }
        self.bind(&p.var, Value::Entity(n.clone())).await
    }

    /// Whether a node type matches a label of a query: the type or a type it extends, the label naming its namespace
    /// or not.
    fn is_label(&self, typ: &str, label: &str) -> bool {
        if eval::label_matches(typ, label) {
            return true;
        }
        let qualified;
        let label = if label.contains('@') {
            label
        } else {
            // as Go does: the namespace of a type read by `domain.NamespaceOf`, which answers the type itself
            qualified = format!("{}@{label}", crate::model::namespace_of(typ));
            &qualified
        };
        typ == label || (self.is_a)(typ, label)
    }

    fn props_match(&self, props: &[(String, Expr)], en: &dyn Entity) -> Result<bool, Flow> {
        if props.is_empty() {
            return Ok(true);
        }
        let mut b = self.top().clone();
        for (k, e) in props {
            let want = eval::eval_now(e, &mut b)?;
            let got = en.property(k).unwrap_or(Value::Null);
            if want.is_null() || !eval::same(&got, &want) {
                return Ok(false);
            }
        }
        Ok(true)
    }

    /// Binds a variable (an equal value when bound already) and checks the conditions it completes. None: refused.
    fn bind<'f>(&'f mut self, name: &'f str, v: Value) -> Fut<'f, Option<Undo>> {
        Box::pin(async move {
            if let Some(old) = self.top().get(name) {
                return Ok(eval::same(old, &v).then_some(None));
            }
            self.top_mut().insert(name.to_string(), v);
            let conds = self.conds.clone();
            for c in conds.iter() {
                if !c.vars.iter().any(|x| x == name) || !c.vars.iter().all(|x| self.top().contains_key(x)) {
                    continue;
                }
                match self.holds(&c.expr).await {
                    Ok(true) => {}
                    Ok(false) => {
                        self.top_mut().remove(name);
                        return Ok(None);
                    }
                    Err(e) => {
                        self.top_mut().remove(name);
                        return Err(e);
                    }
                }
            }
            Ok(Some(Some(name.to_string())))
        })
    }

    fn holds<'f>(&'f mut self, e: &'f Expr) -> Fut<'f, bool> {
        Box::pin(async move {
            let b = self.top().clone();
            let mut live = Live { run: self, b };
            Ok(eval::eval(e, &mut live).await?.truthy())
        })
    }

    /// Follows a relationship pattern from the trail's node, binding its variable and the next node pattern; a
    /// variable length one walks up to its range.
    async fn step(&mut self, s: &Side) -> Result<(), Flow> {
        let from = self.trails[s.tr].nodes[s.k].clone().expect("a node bound");
        let rel = s.rel();
        let Some(range) = rel.range else {
            let hops = self.hops(&from, &rel.types).await?;
            for h in hops.iter() {
                if !self.usable(s, h)? {
                    continue;
                }
                self.used_mut().insert(h.link.id.clone());
                let v = Value::Entity(Arc::new(QLink(h.link.clone())));
                let r = self
                    .finish(s, h.other.clone(), vec![h.link.clone()], Vec::new(), v)
                    .await;
                self.used_mut().remove(&h.link.id);
                r?;
            }
            return Ok(());
        };
        let (lo, mut hi) = (range.min, range.max);
        if hi == 0 || hi > QUERY_MAX_DEPTH {
            if hi > QUERY_MAX_DEPTH {
                return Err(invalid(format!(
                    "a variable length relationship goes at most {QUERY_MAX_DEPTH} hops"
                ))
                .into());
            }
            hi = QUERY_MAX_DEPTH;
            note(
                &mut self.notes,
                format!("an unbounded * is read as *{lo}..{QUERY_MAX_DEPTH}"),
            );
        }
        let mut links = Vec::new();
        let mut mids = Vec::new();
        self.walk(s, from, 0, (lo, hi), &mut links, &mut mids).await
    }

    fn walk<'f>(
        &'f mut self,
        s: &'f Side,
        at: Arc<QNode>,
        depth: usize,
        range: (usize, usize),
        links: &'f mut Vec<Link>,
        mids: &'f mut Vec<Arc<QNode>>,
    ) -> Fut<'f, ()> {
        Box::pin(async move {
            if depth >= range.0 {
                let list = Value::List(
                    links
                        .iter()
                        .map(|l| Value::Entity(Arc::new(QLink(l.clone()))))
                        .collect(),
                );
                // the last node is the end, not a node between
                let inner = mids[..mids.len().saturating_sub(1)].to_vec();
                self.finish(s, at.clone(), links.clone(), inner, list).await?;
            }
            if depth == range.1 {
                return Ok(());
            }
            let hops = self.hops(&at, &s.rel().types).await?;
            for h in hops.iter() {
                if !self.usable(s, h)? {
                    continue;
                }
                self.used_mut().insert(h.link.id.clone());
                links.push(h.link.clone());
                mids.push(h.other.clone());
                let r = self.walk(s, h.other.clone(), depth + 1, range, links, mids).await;
                links.pop();
                mids.pop();
                self.used_mut().remove(&h.link.id);
                r?;
            }
            Ok(())
        })
    }

    fn used_mut(&mut self) -> &mut HashSet<LinkId> {
        self.used.last_mut().expect("a clause")
    }

    /// Whether a hop goes the way the relationship does, unused and with its properties.
    fn usable(&self, s: &Side, h: &Hop) -> Result<bool, Flow> {
        let dir = s.dir();
        if self.used.last().is_some_and(|u| u.contains(&h.link.id))
            || (dir == Direction::Right && !h.out)
            || (dir == Direction::Left && !h.into)
        {
            return Ok(false);
        }
        self.props_match(&s.rel().props, &QLink(h.link.clone()))
    }

    /// Binds a relationship's variable and the next node, then goes on along the pattern.
    async fn finish(
        &mut self,
        s: &Side,
        to: Arc<QNode>,
        mut links: Vec<Link>,
        mut mids: Vec<Arc<QNode>>,
        rel_value: Value,
    ) -> Result<(), Flow> {
        let rel = s.rel();
        let mut undo_rel = None;
        if !rel.var.is_empty() {
            match self.bind(&rel.var, rel_value).await? {
                Some(u) => undo_rel = u,
                None => return Ok(()),
            }
        }
        let r = match self.bind_node(s.target(), &to).await {
            Ok(Some(u)) => {
                let t = &mut self.trails[s.tr];
                let r = if s.right {
                    t.nodes[s.k + 1] = Some(to);
                    t.rels[s.k] = links;
                    t.mids[s.k] = mids;
                    self.solve(frame(
                        Goal::Right {
                            ps: s.ps.clone(),
                            pi: s.pi,
                            k: s.k + 1,
                            tr: s.tr,
                        },
                        s.next.clone(),
                    ))
                    .await
                } else {
                    links.reverse();
                    mids.reverse();
                    t.nodes[s.k - 1] = Some(to);
                    t.rels[s.k - 1] = links;
                    t.mids[s.k - 1] = mids;
                    self.solve(frame(
                        Goal::Left {
                            ps: s.ps.clone(),
                            pi: s.pi,
                            k: s.k - 1,
                            tr: s.tr,
                        },
                        s.next.clone(),
                    ))
                    .await
                };
                self.undo(u);
                r
            }
            Ok(None) => Ok(()),
            Err(e) => Err(e),
        };
        self.undo(undo_rel);
        r
    }

    /// The links of some types of a node in the view (none: every type) and the nodes at their other ends: one
    /// neighbourhood read, kept for the query.
    async fn hops(&mut self, n: &Arc<QNode>, types: &[String]) -> Result<Arc<Vec<Hop>>, Flow> {
        let key = format!("{}\0{}", n.0.id, types.join("|"));
        if let Some(hs) = self.hoods.get(&key) {
            return Ok(hs.clone());
        }
        self.read()?;
        let types = if types.is_empty() {
            None
        } else {
            self.w.link_types(Some(types))
        };
        let nb = self.w.neighbourhood(&n.0.id, types.as_deref()).await?;
        let mut near: HashMap<NodeId, Arc<QNode>> = HashMap::new();
        for m in nb.nodes {
            let id = m.id.clone();
            if let Some(q) = self.keep(m) {
                near.insert(id, q);
            }
        }
        near.insert(n.0.id.clone(), n.clone());
        let mut hs = Vec::new();
        for l in nb.links {
            let Some(to) = l.to_node() else { continue };
            let out = l.from.id == n.0.id;
            let into = to.id == n.0.id;
            let other = if out { &to.id } else { &l.from.id };
            let Some(o) = near.get(other).cloned() else { continue };
            self.links.insert(l.id.clone(), l.clone());
            hs.push(Hop {
                link: l,
                other: o,
                out,
                into,
            });
        }
        let hs = Arc::new(hs);
        self.hoods.insert(key, hs.clone());
        Ok(hs)
    }
}

/// A row being matched: its variables, and the graph its EXISTS match in.
struct Live<'l, 'r, 'w, T> {
    run: &'l mut Run<'r, 'w, T>,
    b: MapScope,
}

impl<T: ReadTx> Scope for Live<'_, '_, '_, T> {
    type Error = Flow;

    fn lookup(&self, name: &str) -> Option<Value> {
        self.b.get(name).cloned()
    }

    fn exists<'a>(&'a mut self, x: &'a Exists) -> EvalFuture<'a, bool, Flow> {
        let b = self.b.clone();
        self.run.exists(x, b)
    }
}

/// The nodes and links the rows name.
#[derive(Default)]
struct Exported {
    nodes: HashSet<NodeId>,
    links: HashSet<LinkId>,
}

impl Exported {
    /// A value of a row as JSON, noting the nodes and links it names.
    fn export(&mut self, v: &Value) -> Json {
        match v {
            Value::Null => Json::Null,
            Value::Bool(b) => Json::Bool(*b),
            Value::Str(s) => Json::String(s.clone()),
            Value::Num(f) => {
                if *f == (*f as i64) as f64 && *f > -1e15 && *f < 1e15 {
                    Json::from(*f as i64)
                } else {
                    serde_json::Number::from_f64(*f).map_or(Json::Null, Json::Number)
                }
            }
            Value::List(xs) => Json::Array(xs.iter().map(|x| self.export(x)).collect()),
            Value::Map(m) => Json::Object(m.iter().map(|(k, x)| (k.clone(), self.export(x))).collect()),
            Value::Entity(e) => {
                if e.kind() == "node" {
                    self.nodes.insert(NodeId(e.id().to_string()));
                    serde_json::json!({"$node": e.id()})
                } else {
                    self.links.insert(LinkId(e.id().to_string()));
                    serde_json::json!({"$link": e.id()})
                }
            }
            Value::Path(p) => {
                let nodes: Vec<Json> = p
                    .nodes
                    .iter()
                    .map(|n| {
                        self.nodes.insert(NodeId(n.id().to_string()));
                        Json::String(n.id().to_string())
                    })
                    .collect();
                let links: Vec<Json> = p
                    .rels
                    .iter()
                    .map(|l| {
                        self.links.insert(LinkId(l.id().to_string()));
                        Json::String(l.id().to_string())
                    })
                    .collect();
                serde_json::json!({"$path": {"nodes": nodes, "links": links}})
            }
        }
    }

    /// The nodes and links the rows name, by key and id: a link's ends read by the query come with it.
    fn finish(mut self, read: &HashMap<NodeId, Arc<QNode>>, links: &HashMap<LinkId, Link>) -> (Vec<Node>, Vec<Link>) {
        let mut out_links: Vec<Link> = Vec::new();
        for id in &self.links {
            let Some(l) = links.get(id) else { continue };
            for end in [Some(&l.from.id), l.to_node().map(|t| &t.id)].into_iter().flatten() {
                if read.contains_key(end) {
                    self.nodes.insert(end.clone());
                }
            }
            out_links.push(l.clone());
        }
        let mut nodes: Vec<Node> = self
            .nodes
            .iter()
            .filter_map(|id| read.get(id).map(|q| q.0.clone()))
            .collect();
        nodes.sort_by(|a, b| a.key.cmp(&b.key));
        out_links.sort_by(|a, b| a.id.cmp(&b.id));
        (nodes, out_links)
    }
}

/// A node of the view as a query sees it.
#[derive(Debug)]
struct QNode(Node);

impl Entity for QNode {
    fn kind(&self) -> &'static str {
        "node"
    }

    fn id(&self) -> &str {
        self.0.id.as_str()
    }

    fn labels(&self) -> Vec<String> {
        vec![self.0.typ.clone()]
    }

    /// The node's own fields first (key, type, state, id, version, namespace, owner, project), then its properties.
    fn property(&self, key: &str) -> Option<Value> {
        let text = |s: &str| {
            if s.is_empty() {
                Value::Null
            } else {
                Value::Str(s.to_string())
            }
        };
        let n = &self.0;
        Some(match key {
            "key" => Value::Str(n.key.clone()),
            "type" => Value::Str(n.typ.clone()),
            "state" => text(&n.state),
            "id" => Value::Str(n.id.to_string()),
            "version" => Value::Num(f64::from(n.version)),
            "namespace" => Value::Str(n.namespace.clone()),
            "owner" => text(&n.owner.key),
            "project" => text(&n.project.key),
            _ => return n.props.as_ref()?.get(key).map(Value::from_json),
        })
    }

    fn properties(&self) -> BTreeMap<String, Value> {
        props(&self.0.props)
    }

    fn as_any(&self) -> &dyn Any {
        self
    }
}

/// A link of the view as a query sees it.
#[derive(Debug)]
struct QLink(Link);

impl Entity for QLink {
    fn kind(&self) -> &'static str {
        "relationship"
    }

    fn id(&self) -> &str {
        self.0.id.as_str()
    }

    fn labels(&self) -> Vec<String> {
        vec![self.0.typ.clone()]
    }

    /// The link's type and id, then its properties.
    fn property(&self, key: &str) -> Option<Value> {
        Some(match key {
            "type" => Value::Str(self.0.typ.clone()),
            "id" => Value::Str(self.0.id.to_string()),
            _ => return self.0.props.as_ref()?.get(key).map(Value::from_json),
        })
    }

    fn properties(&self) -> BTreeMap<String, Value> {
        props(&self.0.props)
    }

    fn as_any(&self) -> &dyn Any {
        self
    }
}

fn props(p: &crate::model::Props) -> BTreeMap<String, Value> {
    p.iter()
        .flat_map(|m| m.iter())
        .map(|(k, v)| (k.clone(), Value::from_json(v)))
        .collect()
}
