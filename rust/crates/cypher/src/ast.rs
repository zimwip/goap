//! The tree of a query (Go's `lib/cypher/ast.go`): what the subset reads of the openCypher grammar.
use std::sync::Arc;

use crate::eval::Value;

/// A read query: one or more MATCH clauses, then RETURN.
#[derive(Debug, Clone)]
pub struct Query {
    pub matches: Vec<Match>,
    pub ret: Return,
}

/// A MATCH clause: its patterns and its WHERE. An OPTIONAL MATCH keeps the rows it matches nothing for, its variables
/// null.
#[derive(Debug, Clone)]
pub struct Match {
    pub optional: bool,
    pub patterns: Arc<Vec<Pattern>>,
    pub filter: Option<Expr>,
}

/// A path pattern: nodes joined by relationships (`rels.len() == nodes.len() - 1`), named by `var` when given.
#[derive(Debug, Clone, Default)]
pub struct Pattern {
    pub var: String,
    pub nodes: Vec<NodePattern>,
    pub rels: Vec<RelPattern>,
}

/// `(v:Label {key: value})`.
#[derive(Debug, Clone, Default)]
pub struct NodePattern {
    pub var: String,
    pub labels: Vec<String>,
    /// the properties asked, each key once (the last written wins)
    pub props: Vec<(String, Expr)>,
}

/// The direction of a relationship pattern, from its left node to its right one.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Direction {
    /// `-[]-`, also `<-[]->`
    #[default]
    Either,
    /// `-[]->`
    Right,
    /// `<-[]-`
    Left,
}

impl Direction {
    /// The direction read from the right node to the left one.
    pub fn reversed(self) -> Direction {
        match self {
            Direction::Right => Direction::Left,
            Direction::Left => Direction::Right,
            Direction::Either => Direction::Either,
        }
    }
}

/// `-[r:TYPE|OTHER*1..3 {key: value}]->`.
#[derive(Debug, Clone, Default)]
pub struct RelPattern {
    pub var: String,
    pub types: Vec<String>,
    pub dir: Direction,
    /// set for a variable length relationship (`*`, `*2`, `*1..3`)
    pub range: Option<Range>,
    pub props: Vec<(String, Expr)>,
}

/// The number of hops of a variable length relationship: `max` 0 is unbounded.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Range {
    pub min: usize,
    pub max: usize,
}

/// The RETURN clause.
#[derive(Debug, Clone, Default)]
pub struct Return {
    pub distinct: bool,
    /// `RETURN *` (followed by the items, if any)
    pub star: bool,
    pub items: Vec<Item>,
    pub order: Vec<SortItem>,
    pub skip: Option<Expr>,
    pub limit: Option<Expr>,
}

/// A projection: an expression and its column name (the alias, else the text of the expression as written).
#[derive(Debug, Clone)]
pub struct Item {
    pub expr: Expr,
    pub name: String,
}

/// An ORDER BY key.
#[derive(Debug, Clone)]
pub struct SortItem {
    pub expr: Expr,
    pub desc: bool,
}

/// A binary operator.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Op {
    Or,
    Xor,
    And,
    Eq,
    Ne,
    Lt,
    Gt,
    Le,
    Ge,
    Add,
    Sub,
    Mul,
    Div,
    Mod,
    Pow,
    In,
    StartsWith,
    EndsWith,
    Contains,
}

impl Op {
    /// The operator as Go's tree spells it (its error messages name it).
    pub fn as_str(self) -> &'static str {
        match self {
            Op::Or => "OR",
            Op::Xor => "XOR",
            Op::And => "AND",
            Op::Eq => "=",
            Op::Ne => "<>",
            Op::Lt => "<",
            Op::Gt => ">",
            Op::Le => "<=",
            Op::Ge => ">=",
            Op::Add => "+",
            Op::Sub => "-",
            Op::Mul => "*",
            Op::Div => "/",
            Op::Mod => "%",
            Op::Pow => "^",
            Op::In => "IN",
            Op::StartsWith => "STARTS WITH",
            Op::EndsWith => "ENDS WITH",
            Op::Contains => "CONTAINS",
        }
    }
}

/// A unary operator: NOT, or the minus sign.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Unary {
    Not,
    Neg,
}

/// An expression.
#[derive(Debug, Clone)]
pub enum Expr {
    /// null, a boolean, a number or a string
    Lit(Value),
    Var(String),
    /// `x.key`
    Prop(Box<Expr>, String),
    /// `x[i]`
    Index(Box<Expr>, Box<Expr>),
    /// `x[from..to]` (open bounds: none)
    Slice(Box<Expr>, Option<Box<Expr>>, Option<Box<Expr>>),
    Unary(Unary, Box<Expr>),
    Binary(Op, Box<Expr>, Box<Expr>),
    /// `x IS NULL`, or `x IS NOT NULL` when true
    IsNull(Box<Expr>, bool),
    /// `n:Label:Other`
    HasLabels(Box<Expr>, Vec<String>),
    List(Vec<Expr>),
    Map(Vec<(String, Expr)>),
    Call(Call),
    Case(Case),
    /// `EXISTS { pattern [WHERE …] }` or a pattern predicate: true when the patterns match, the variables bound
    /// already being those of the row, the others local to it.
    Exists(Arc<Exists>),
}

/// A function call; `star` is `count(*)`.
#[derive(Debug, Clone)]
pub struct Call {
    pub name: String,
    pub distinct: bool,
    pub star: bool,
    pub args: Vec<Expr>,
}

/// `CASE [subject] WHEN … THEN … [ELSE …] END`.
#[derive(Debug, Clone)]
pub struct Case {
    pub subject: Option<Box<Expr>>,
    pub whens: Vec<Expr>,
    pub thens: Vec<Expr>,
    pub otherwise: Option<Box<Expr>>,
}

/// The patterns an EXISTS matches, and its WHERE.
#[derive(Debug, Clone)]
pub struct Exists {
    pub patterns: Arc<Vec<Pattern>>,
    pub filter: Option<Expr>,
}

/// The aggregating functions RETURN groups by.
pub const AGGREGATES: [&str; 6] = ["count", "collect", "sum", "avg", "min", "max"];

impl Expr {
    /// The expressions it holds (an EXISTS: its property values and its WHERE).
    fn children(&self) -> Vec<&Expr> {
        match self {
            Expr::Prop(x, _) | Expr::Unary(_, x) | Expr::IsNull(x, _) | Expr::HasLabels(x, _) => vec![x],
            Expr::Index(x, i) => vec![x, i],
            Expr::Slice(x, from, to) => {
                let mut out: Vec<&Expr> = vec![x];
                out.extend(from.as_deref());
                out.extend(to.as_deref());
                out
            }
            Expr::Binary(_, l, r) => vec![l, r],
            Expr::List(xs) => xs.iter().collect(),
            Expr::Map(kv) => kv.iter().map(|(_, v)| v).collect(),
            Expr::Call(c) => c.args.iter().collect(),
            Expr::Case(c) => {
                let mut out: Vec<&Expr> = c.subject.as_deref().into_iter().collect();
                for (w, t) in c.whens.iter().zip(&c.thens) {
                    out.push(w);
                    out.push(t);
                }
                out.extend(c.otherwise.as_deref());
                out
            }
            Expr::Exists(x) => {
                let mut out: Vec<&Expr> = x.patterns.iter().flat_map(pattern_exprs).collect();
                out.extend(x.filter.as_ref());
                out
            }
            Expr::Lit(_) | Expr::Var(_) => Vec::new(),
        }
    }

    /// Visits the expression and what it holds, depth first, while `visit` answers true.
    pub fn walk(&self, visit: &mut impl FnMut(&Expr) -> bool) {
        if !visit(self) {
            return;
        }
        for c in self.children() {
            c.walk(visit);
        }
    }

    /// The variables it reads, in order (Go's `Vars`): an EXISTS reads the variables of its patterns.
    pub fn vars(&self) -> Vec<String> {
        let mut out = Vec::new();
        self.collect_vars(&mut out);
        out
    }

    fn collect_vars(&self, out: &mut Vec<String>) {
        fn add(out: &mut Vec<String>, v: &str) {
            if !out.iter().any(|o| o == v) {
                out.push(v.to_string());
            }
        }
        match self {
            Expr::Var(name) => add(out, name),
            Expr::Exists(x) => {
                for p in x.patterns.iter() {
                    for v in p.vars() {
                        add(out, &v);
                    }
                    for e in pattern_exprs(p) {
                        e.collect_vars(out);
                    }
                }
                if let Some(w) = &x.filter {
                    w.collect_vars(out);
                }
            }
            _ => {
                for c in self.children() {
                    c.collect_vars(out);
                }
            }
        }
    }

    /// The variables the EXISTS it holds name in their patterns: bound there when the row has not (Go's
    /// `Declared`).
    pub fn declared(&self) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        self.walk(&mut |x| {
            if let Expr::Exists(ex) = x {
                for p in ex.patterns.iter() {
                    for v in p.vars() {
                        if !out.contains(&v) {
                            out.push(v);
                        }
                    }
                }
            }
            true
        });
        out
    }

    /// Whether it holds an EXISTS anywhere.
    pub fn has_exists(&self) -> bool {
        let mut found = false;
        self.walk(&mut |x| {
            if matches!(x, Expr::Exists(_)) {
                found = true;
            }
            !found
        });
        found
    }

    /// Whether it is an aggregating call.
    pub fn is_aggregate(&self) -> bool {
        matches!(self, Expr::Call(c) if AGGREGATES.contains(&c.name.as_str()))
    }

    /// Whether it holds an aggregating call anywhere outside an EXISTS (Go's `HasAggregate`).
    pub fn has_aggregate(&self) -> bool {
        if self.is_aggregate() {
            return true;
        }
        match self {
            Expr::Exists(_) => false,
            _ => self.children().into_iter().any(Expr::has_aggregate),
        }
    }

    /// Rebuilds the expression bottom up, each expression replaced by what `f` answers; an EXISTS is replaced as a
    /// whole, what it holds left alone (Go's `Rewrite`).
    pub fn rewrite(self, f: &mut impl FnMut(Expr) -> Expr) -> Expr {
        let b = |x: Box<Expr>, f: &mut dyn FnMut(Expr) -> Expr| Box::new(rewrite_dyn(*x, f));
        let e = match self {
            Expr::Prop(x, k) => Expr::Prop(b(x, f), k),
            Expr::Index(x, i) => {
                let x = b(x, f);
                Expr::Index(x, b(i, f))
            }
            Expr::Slice(x, from, to) => {
                let x = b(x, f);
                let from = from.map(|e| b(e, f));
                Expr::Slice(x, from, to.map(|e| b(e, f)))
            }
            Expr::Unary(op, x) => Expr::Unary(op, b(x, f)),
            Expr::Binary(op, l, r) => {
                let l = b(l, f);
                Expr::Binary(op, l, b(r, f))
            }
            Expr::IsNull(x, not) => Expr::IsNull(b(x, f), not),
            Expr::HasLabels(x, ls) => Expr::HasLabels(b(x, f), ls),
            Expr::List(xs) => Expr::List(xs.into_iter().map(|x| rewrite_dyn(x, f)).collect()),
            Expr::Map(kv) => Expr::Map(kv.into_iter().map(|(k, v)| (k, rewrite_dyn(v, f))).collect()),
            Expr::Call(mut c) => {
                c.args = c.args.into_iter().map(|x| rewrite_dyn(x, f)).collect();
                Expr::Call(c)
            }
            Expr::Case(c) => {
                let subject = c.subject.map(|e| b(e, f));
                let whens = c.whens.into_iter().map(|x| rewrite_dyn(x, f)).collect();
                let thens = c.thens.into_iter().map(|x| rewrite_dyn(x, f)).collect();
                let otherwise = c.otherwise.map(|e| b(e, f));
                Expr::Case(Case {
                    subject,
                    whens,
                    thens,
                    otherwise,
                })
            }
            e => e,
        };
        f(e)
    }
}

fn rewrite_dyn(e: Expr, f: &mut dyn FnMut(Expr) -> Expr) -> Expr {
    e.rewrite(&mut |x| f(x))
}

/// The expression split on its top-level ANDs (none: none).
pub fn conjuncts(e: Option<&Expr>) -> Vec<&Expr> {
    fn split<'a>(e: &'a Expr, out: &mut Vec<&'a Expr>) {
        match e {
            Expr::Binary(Op::And, l, r) => {
                split(l, out);
                split(r, out);
            }
            _ => out.push(e),
        }
    }
    let mut out = Vec::new();
    if let Some(e) = e {
        split(e, &mut out);
    }
    out
}

impl Pattern {
    /// The variables it names: its nodes, its relationships and the path (Go's `PatternVars`).
    pub fn vars(&self) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        let mut add = |v: &str| {
            if !v.is_empty() && !out.iter().any(|o| o == v) {
                out.push(v.to_string());
            }
        };
        for (i, n) in self.nodes.iter().enumerate() {
            add(&n.var);
            if let Some(r) = self.rels.get(i) {
                add(&r.var);
            }
        }
        add(&self.var);
        out
    }
}

/// The property values of a pattern.
fn pattern_exprs(p: &Pattern) -> impl Iterator<Item = &Expr> {
    p.nodes
        .iter()
        .flat_map(|n| n.props.iter().map(|(_, e)| e))
        .chain(p.rels.iter().flat_map(|r| r.props.iter().map(|(_, e)| e)))
}
