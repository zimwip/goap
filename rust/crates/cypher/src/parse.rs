//! A query of the subset read into its tree (Go's `lib/cypher/parse.go`), by descent over the rules of the openCypher
//! grammar the subset keeps, with the grammar's white space rules. What the subset leaves out is refused in Go's
//! words; what the grammar refuses, in this parser's own, at the furthest token it read (ADR 0163: the words of
//! ANTLR's errors were the Go service's).
use std::sync::Arc;

use crate::ast::{
    AGGREGATES, Call, Case, Direction, Exists, Expr, Item, Match, NodePattern, Op, Pattern, Query, Range, RelPattern,
    Return, SortItem, Unary,
};
use crate::eval::{Value, function};
use crate::lex::{DASH, LEFT_HEAD, RIGHT_HEAD, Tok, Token};

/// The longest query Parse reads (Go's `MaxQueryBytes`).
pub const MAX_QUERY_BYTES: usize = 16 << 10;

/// A query the subset does not read (`cypher.Error`): a construct it leaves out in Go's words, or the grammar's
/// refusal of the query in this parser's words, at a line and a column of the query (1-based; 0: the query as a whole).
#[derive(Debug, Clone, PartialEq)]
pub struct Error {
    pub line: usize,
    pub col: usize,
    pub msg: String,
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        if self.line == 0 {
            return f.write_str(&self.msg);
        }
        write!(f, "{}:{}: {}", self.line, self.col, self.msg)
    }
}

impl Error {
    /// The error at a byte of the query: its line, and its column counted in characters, as the grammar's.
    fn at(src: &str, pos: usize, msg: String) -> Error {
        let before = &src[..pos];
        let line_start = before.rfind('\n').map_or(0, |n| n + 1);
        Error {
            line: before.matches('\n').count() + 1,
            col: before[line_start..].chars().count() + 1,
            msg,
        }
    }
}

/// Reads a query of the subset; None for a query not read here.
pub fn parse(src: &str) -> Option<Query> {
    read(src).ok()
}

/// Reads a query of the subset: the query, else its refusal: what the subset leaves out in Go's words (the whole
/// query read as the grammar reads it, the construct Go refuses first named), what the grammar refuses in this
/// parser's, at the furthest token read.
pub fn read(src: &str) -> Result<Query, Error> {
    if src.len() > MAX_QUERY_BYTES {
        let msg = format!("a query is at most {MAX_QUERY_BYTES} bytes");
        return Err(Error { line: 0, col: 0, msg });
    }
    if src.trim().is_empty() {
        let msg = "empty query".to_string();
        return Err(Error { line: 0, col: 0, msg });
    }
    let lexed = crate::lex::lex(src).map_err(|e| Error::at(src, e.pos, e.msg))?;
    let mut p = Parser {
        src,
        t: lexed.tokens,
        i: 0,
        far: std::cell::Cell::new(0),
        refused: Vec::new(),
    };
    let Some(q) = p.query() else {
        // the lexer's refusal of a `@` comes first when it stands before the parser's
        let e = p.syntax_error();
        return Err(match lexed.stray {
            Some(at) if at < p.t[p.far.get().min(p.t.len() - 1)].start => {
                Error::at(src, at, "token recognition error at: '@'".into())
            }
            _ => e,
        });
    };
    // the grammar's lexer refuses a `@` within a name before the parser reads past it
    if let Some(at) = lexed.stray {
        return Err(Error::at(src, at, "token recognition error at: '@'".into()));
    }
    // the refusal Go's build meets first: a UNION, then a WITH, then a write, then the first in the order it reads
    if let Some((_, at, what)) = p.refused.iter().min_by_key(|(stage, _, _)| *stage) {
        let msg = format!("{what} is not supported: the query language is read-only MATCH … RETURN (ADR 0160)");
        return Err(Error::at(src, *at, msg));
    }
    q.ok_or_else(|| p.syntax_error())
}

/// When Go's build refuses a construct, first to last: a UNION, the first WITH, the first write, then the rest in
/// the order of the query.
const UNION: u8 = 0;
const WITH: u8 = 1;
const WRITE: u8 = 2;
const READ: u8 = 3;

struct Parser<'s> {
    src: &'s str,
    t: Vec<Token>,
    i: usize,
    /// the furthest token read: where the grammar is refused when no rule reads the query
    far: std::cell::Cell<usize>,
    /// the constructs read that Go refuses: when, where, what
    refused: Vec<(u8, usize, String)>,
}

impl Parser<'_> {
    fn peek(&self) -> &Token {
        self.far.set(self.far.get().max(self.i));
        &self.t[self.i]
    }

    /// The refusal of the grammar, at the furthest token read.
    fn syntax_error(&self) -> Error {
        let t = &self.t[self.far.get().min(self.t.len() - 1)];
        let msg = match t.tok {
            Tok::Eof => "the query ends where more is expected".to_string(),
            _ => format!("unexpected '{}'", &self.src[t.start..t.end]),
        };
        Error::at(self.src, t.start, msg)
    }

    fn next_tok(&self) -> &Token {
        &self.t[(self.i + 1).min(self.t.len() - 1)]
    }

    fn bump(&mut self) -> &Token {
        self.far.set(self.far.get().max(self.i));
        let t = &self.t[self.i];
        if self.i < self.t.len() - 1 {
            self.i += 1;
        }
        t
    }

    fn sp(&self) -> bool {
        self.peek().sp
    }

    fn punct(&self, p: &str) -> bool {
        matches!(self.peek().tok, Tok::Punct(x) if x == p)
    }

    fn eat(&mut self, p: &str) -> bool {
        if self.punct(p) {
            self.bump();
            return true;
        }
        false
    }

    fn expect(&mut self, p: &str) -> Option<()> {
        self.eat(p).then_some(())
    }

    fn kw(&self, k: &str) -> bool {
        self.peek().tok.is(k)
    }

    fn expect_kw(&mut self, k: &str) -> Option<()> {
        if self.kw(k) {
            self.bump();
            return Some(());
        }
        None
    }

    /// The end of the token read last.
    fn prev_end(&self) -> usize {
        self.t[self.i.saturating_sub(1)].end
    }

    fn refuse(&mut self, stage: u8, at: usize, what: impl Into<String>) {
        self.refused.push((stage, at, what.into()));
    }

    /// The query: its single queries joined by UNION; Some(None) for a query read but not of the subset (refused).
    fn query(&mut self) -> Option<Option<Query>> {
        let q = self.regular_query()?;
        self.eat(";");
        matches!(self.peek().tok, Tok::Eof).then_some(q)
    }

    /// Single queries joined by UNION (`oC_RegularQuery`).
    fn regular_query(&mut self) -> Option<Option<Query>> {
        let q = self.single_query()?;
        while self.kw("UNION") {
            let at = self.peek().start;
            self.bump();
            if self.kw("ALL") {
                if !self.sp() {
                    return None;
                }
                self.bump();
            }
            self.refuse(UNION, at, "UNION");
            self.single_query()?;
        }
        Some(q)
    }

    /// A single query: parts ended by WITH, then reading clauses and RETURN, or writes and an optional RETURN. Some
    /// query when it is MATCH clauses and RETURN only.
    fn single_query(&mut self) -> Option<Option<Query>> {
        let mut part = self.peek().start;
        let mut matches = Vec::new();
        let mut subset = true;
        let mut matched = false;
        let mut writes = false;
        loop {
            if self.kw("OPTIONAL") || self.kw("MATCH") {
                if writes {
                    return None;
                }
                matches.push(self.match_clause()?);
                matched = true;
            } else if self.kw("UNWIND") {
                if writes {
                    return None;
                }
                let at = self.peek().start;
                self.bump();
                self.expr()?;
                self.as_variable()?;
                self.refuse(READ, at, "UNWIND");
                subset = false;
            } else if let Some((at, word)) = self.write_clause()? {
                self.refuse(WRITE, at, format!("a write ({word})"));
                writes = true;
                subset = false;
            } else if self.kw("WITH") {
                let at = self.peek().start;
                self.bump();
                self.projection()?;
                self.filter()?;
                self.refuse(WITH, at, "WITH");
                subset = false;
                writes = false;
                matched = false;
                part = self.peek().start;
            } else {
                break;
            }
        }
        if !self.kw("RETURN") {
            return writes.then_some(None);
        }
        if !matched && !writes {
            self.refuse(READ, part, "a query without MATCH");
        }
        self.bump();
        let ret = self.projection()?;
        Some(subset.then_some(Query { matches, ret }))
    }

    fn match_clause(&mut self) -> Option<Match> {
        let optional = self.kw("OPTIONAL");
        if optional {
            self.bump();
            if !self.sp() {
                return None;
            }
        }
        self.expect_kw("MATCH")?;
        let patterns = self.patterns()?;
        let filter = self.filter()?;
        Some(Match {
            optional,
            patterns: Arc::new(patterns),
            filter,
        })
    }

    /// `SP AS SP variable`.
    fn as_variable(&mut self) -> Option<String> {
        if !self.sp() || !self.kw("AS") {
            return None;
        }
        self.bump();
        if !self.sp() {
            return None;
        }
        self.variable()
    }

    /// A write clause, read as the grammar reads it (CREATE, MERGE, DELETE, SET, REMOVE): where it starts and its
    /// first word as Go names it, None when the clause is no write, refused when the grammar does not read it.
    fn write_clause(&mut self) -> Option<Option<(usize, String)>> {
        let start = self.i;
        if self.kw("CREATE") {
            self.bump();
            self.patterns()?;
        } else if self.kw("MERGE") {
            self.bump();
            self.pattern_part()?;
            while self.sp() && self.kw("ON") {
                self.bump();
                if !self.sp() || !(self.kw("MATCH") || self.kw("CREATE")) {
                    return None;
                }
                self.bump();
                if !self.sp() {
                    return None;
                }
                self.set_clause()?;
            }
        } else if self.kw("DELETE") || self.kw("DETACH") {
            if self.kw("DETACH") {
                self.bump();
                if !self.sp() {
                    return None;
                }
            }
            self.expect_kw("DELETE")?;
            self.expr()?;
            while self.eat(",") {
                self.expr()?;
            }
        } else if self.kw("SET") {
            self.set_clause()?;
        } else if self.kw("REMOVE") {
            self.bump();
            if !self.sp() {
                return None;
            }
            self.remove_item()?;
            while self.eat(",") {
                self.remove_item()?;
            }
        } else {
            return Some(None);
        }
        // Go's `firstWord`: the clause's text up to its first white space, in capitals
        let at = self.t[start].start;
        let text = &self.src[at..self.prev_end()];
        let word = text.split(char::is_whitespace).next().unwrap_or_default();
        Some(Some((at, word.to_uppercase())))
    }

    /// `SET item, …`: a property, a variable set or added to, or labels.
    fn set_clause(&mut self) -> Option<()> {
        self.expect_kw("SET")?;
        loop {
            if self.symbolic_at(self.i) && matches!(self.next_tok().tok, Tok::Punct("=" | "+=" | ":")) {
                self.variable()?;
                if self.punct(":") {
                    self.labels()?;
                } else {
                    self.bump();
                    self.expr()?;
                }
            } else {
                self.property_expression()?;
                self.expect("=")?;
                self.expr()?;
            }
            if !self.eat(",") {
                return Some(());
            }
        }
    }

    /// A variable and labels, or a property.
    fn remove_item(&mut self) -> Option<()> {
        if self.symbolic_at(self.i) && matches!(self.next_tok().tok, Tok::Punct(":")) {
            self.variable()?;
            self.labels()?;
            return Some(());
        }
        self.property_expression()
    }

    /// An atom and one property lookup or more.
    fn property_expression(&mut self) -> Option<()> {
        self.atom()?;
        self.expect(".")?;
        self.schema_name()?;
        while self.eat(".") {
            self.schema_name()?;
        }
        Some(())
    }

    /// Whether the token at `i` is a symbolic name of the grammar: a name no keyword but COUNT, FILTER, EXTRACT, ANY,
    /// NONE and SINGLE, or one between backticks.
    fn symbolic_at(&self, i: usize) -> bool {
        match &self.t[i].tok {
            Tok::Escaped(_) => true,
            t @ Tok::Word(_) => {
                !t.is_keyword()
                    || ["COUNT", "FILTER", "EXTRACT", "ANY", "NONE", "SINGLE"]
                        .iter()
                        .any(|k| t.is(k))
            }
            _ => false,
        }
    }

    /// `WHERE SP expression`, when there is one.
    fn filter(&mut self) -> Option<Option<Expr>> {
        if !self.kw("WHERE") {
            return Some(None);
        }
        self.bump();
        if !self.sp() {
            return None;
        }
        Some(Some(self.expr()?))
    }

    fn patterns(&mut self) -> Option<Vec<Pattern>> {
        let mut out = vec![self.pattern_part()?];
        while self.eat(",") {
            out.push(self.pattern_part()?);
        }
        Some(out)
    }

    fn pattern_part(&mut self) -> Option<Pattern> {
        let mut var = String::new();
        if matches!(self.next_tok().tok, Tok::Punct("=")) {
            var = self.variable()?;
            self.expect("=")?;
        }
        if self.punct("(") && matches!(self.next_tok().tok, Tok::Punct("(")) {
            return None; // a pattern between parentheses: Go's
        }
        let mut p = self.chain()?;
        p.var = var;
        Some(p)
    }

    /// A node pattern and the relationships and nodes chained to it.
    fn chain(&mut self) -> Option<Pattern> {
        let mut p = Pattern {
            nodes: vec![self.node_pattern()?],
            ..Pattern::default()
        };
        while self.left_head() || self.dash() {
            p.rels.push(self.rel_pattern()?);
            p.nodes.push(self.node_pattern()?);
        }
        Some(p)
    }

    fn node_pattern(&mut self) -> Option<NodePattern> {
        self.expect("(")?;
        let mut n = NodePattern::default();
        if matches!(self.peek().tok, Tok::Word(_) | Tok::Escaped(_)) {
            n.var = self.variable()?;
        }
        if self.punct(":") {
            n.labels = self.labels()?;
        }
        if self.punct("{") {
            n.props = self.props()?;
        } else if self.punct("$") {
            self.props_parameter()?;
        }
        self.expect(")")?;
        Some(n)
    }

    fn labels(&mut self) -> Option<Vec<String>> {
        let mut out = Vec::new();
        while self.eat(":") {
            out.push(self.schema_name()?);
        }
        Some(out)
    }

    /// A dash of a pattern (`oC_Dash`) and its left arrow head (`oC_LeftArrowHead`), ASCII or not.
    fn dash(&self) -> bool {
        self.punct("-") || self.punct(DASH)
    }

    fn left_head(&self) -> bool {
        self.punct("<") || self.punct(LEFT_HEAD)
    }

    fn expect_dash(&mut self) -> Option<()> {
        if self.dash() {
            self.bump();
            return Some(());
        }
        None
    }

    fn rel_pattern(&mut self) -> Option<RelPattern> {
        let left = self.left_head();
        if left {
            self.bump();
        }
        self.expect_dash()?;
        let mut r = RelPattern::default();
        if self.eat("[") {
            if matches!(self.peek().tok, Tok::Word(_) | Tok::Escaped(_)) {
                r.var = self.variable()?;
            }
            if self.eat(":") {
                r.types.push(self.schema_name()?);
                while self.eat("|") {
                    if self.punct(":") {
                        if self.sp() {
                            return None;
                        }
                        self.bump();
                    }
                    r.types.push(self.schema_name()?);
                }
            }
            if self.eat("*") {
                r.range = Some(self.range()?);
            }
            if self.punct("{") {
                r.props = self.props()?;
            } else if self.punct("$") {
                self.props_parameter()?;
            }
            self.expect("]")?;
        }
        self.expect_dash()?;
        let right = self.punct(">") || self.punct(RIGHT_HEAD);
        if right {
            self.bump();
        }
        r.dir = match (left, right) {
            (true, false) => Direction::Left,
            (false, true) => Direction::Right,
            _ => Direction::Either,
        };
        Some(r)
    }

    /// `*`, `*n`, `*n..`, `*..m`, `*n..m` once the star is read (Go's `buildRange`).
    fn range(&mut self) -> Option<Range> {
        let bound = |p: &mut Self| -> Option<Option<usize>> {
            let t = p.peek().clone();
            match t.tok {
                Tok::Int(v) if (0..=1 << 20).contains(&v) => {
                    p.bump();
                    Some(Some(v as usize))
                }
                Tok::Int(_) | Tok::Huge(_) => {
                    p.bump();
                    p.refuse(READ, t.start, format!("the integer {}", &p.src[t.start..t.end]));
                    Some(Some(0))
                }
                _ => Some(None),
            }
        };
        let first = bound(self)?;
        let dots = self.eat("..");
        let second = if dots { bound(self)? } else { None };
        let mut r = Range { min: 1, max: 0 };
        match (first, dots, second) {
            (Some(a), true, Some(b)) => (r.min, r.max) = (a, b),
            (Some(a), false, _) => (r.min, r.max) = (a, a),
            (Some(a), true, None) => r.min = a,
            (None, true, Some(b)) => r.max = b,
            _ => {}
        }
        if r.max != 0 && r.max < r.min {
            return None;
        }
        Some(r)
    }

    /// Properties given as a parameter, which Go refuses.
    fn props_parameter(&mut self) -> Option<()> {
        let start = self.peek().start;
        self.parameter()?;
        self.refuse(READ, start, "a parameter");
        Some(())
    }

    fn props(&mut self) -> Option<Vec<(String, Expr)>> {
        let kv = self.map_literal()?;
        let mut out: Vec<(String, Expr)> = Vec::new();
        for (k, v) in kv {
            out.retain(|(o, _)| *o != k);
            out.push((k, v));
        }
        Some(out)
    }

    fn map_literal(&mut self) -> Option<Vec<(String, Expr)>> {
        self.expect("{")?;
        let mut out = Vec::new();
        if self.eat("}") {
            return Some(out);
        }
        loop {
            let k = self.schema_name()?;
            self.expect(":")?;
            out.push((k, self.expr()?));
            if self.eat("}") {
                return Some(out);
            }
            self.expect(",")?;
        }
    }

    /// A variable: a symbolic name, not a keyword.
    fn variable(&mut self) -> Option<String> {
        match &self.peek().tok {
            Tok::Word(w) if !self.peek().tok.is_keyword() => {
                let w = w.clone();
                self.bump();
                Some(w)
            }
            Tok::Escaped(w) => {
                let w = w.clone();
                self.bump();
                Some(w)
            }
            _ => None,
        }
    }

    /// A label, a relationship type or a property key: any name, keywords included.
    fn schema_name(&mut self) -> Option<String> {
        match &self.peek().tok {
            Tok::Word(w) | Tok::Escaped(w) => {
                let w = w.clone();
                self.bump();
                Some(w)
            }
            _ => None,
        }
    }

    fn projection(&mut self) -> Option<Return> {
        let mut r = Return::default();
        if self.kw("DISTINCT") {
            self.bump();
            r.distinct = true;
        }
        if !self.sp() {
            return None;
        }
        if self.eat("*") {
            r.star = true;
            while self.eat(",") {
                r.items.push(self.item()?);
            }
        } else {
            r.items.push(self.item()?);
            while self.eat(",") {
                r.items.push(self.item()?);
            }
        }
        if self.kw("ORDER") {
            if !self.sp() {
                return None;
            }
            self.bump();
            if !self.sp() {
                return None;
            }
            self.expect_kw("BY")?;
            if !self.sp() {
                return None;
            }
            r.order.push(self.sort_item()?);
            while self.punct(",") {
                if self.sp() {
                    return None;
                }
                self.bump();
                r.order.push(self.sort_item()?);
            }
        }
        if self.kw("SKIP") {
            r.skip = Some(self.clause_expr()?);
        }
        if self.kw("LIMIT") {
            r.limit = Some(self.clause_expr()?);
        }
        Some(r)
    }

    /// `SP SKIP SP expression` or `SP LIMIT SP expression`.
    fn clause_expr(&mut self) -> Option<Expr> {
        if !self.sp() {
            return None;
        }
        self.bump();
        if !self.sp() {
            return None;
        }
        self.expr()
    }

    fn item(&mut self) -> Option<Item> {
        let start = self.peek().start;
        let expr = self.expr()?;
        let mut name = self.src[start..self.prev_end()].to_string();
        if self.kw("AS") {
            if !self.sp() {
                return None;
            }
            self.bump();
            if !self.sp() {
                return None;
            }
            name = self.variable()?;
        }
        Some(Item { expr, name })
    }

    fn sort_item(&mut self) -> Option<SortItem> {
        let expr = self.expr()?;
        let mut desc = false;
        if self.kw("DESC") || self.kw("DESCENDING") {
            self.bump();
            desc = true;
        } else if self.kw("ASC") || self.kw("ASCENDING") {
            self.bump();
        }
        Some(SortItem { expr, desc })
    }

    fn expr(&mut self) -> Option<Expr> {
        let mut x = self.xor()?;
        while self.kw("OR") && self.sp() && self.next_tok().sp {
            self.bump();
            x = Expr::Binary(Op::Or, Box::new(x), Box::new(self.xor()?));
        }
        Some(x)
    }

    fn xor(&mut self) -> Option<Expr> {
        let mut x = self.and()?;
        while self.kw("XOR") && self.sp() && self.next_tok().sp {
            self.bump();
            x = Expr::Binary(Op::Xor, Box::new(x), Box::new(self.and()?));
        }
        Some(x)
    }

    fn and(&mut self) -> Option<Expr> {
        let mut x = self.not()?;
        while self.kw("AND") && self.sp() && self.next_tok().sp {
            self.bump();
            x = Expr::Binary(Op::And, Box::new(x), Box::new(self.not()?));
        }
        Some(x)
    }

    fn not(&mut self) -> Option<Expr> {
        let mut nots = 0;
        while self.kw("NOT") {
            self.bump();
            nots += 1;
        }
        let mut x = self.comparison()?;
        for _ in 0..nots {
            x = Expr::Unary(Unary::Not, Box::new(x));
        }
        Some(x)
    }

    /// `a < b <= c` read as `a < b AND b <= c`.
    fn comparison(&mut self) -> Option<Expr> {
        let mut left = self.predicate()?;
        let mut out: Option<Expr> = None;
        loop {
            let op = match self.peek().tok {
                Tok::Punct("=") => Op::Eq,
                Tok::Punct("<>") => Op::Ne,
                Tok::Punct("<=") => Op::Le,
                Tok::Punct(">=") => Op::Ge,
                Tok::Punct("<") => Op::Lt,
                Tok::Punct(">") => Op::Gt,
                _ => break,
            };
            self.bump();
            let right = self.predicate()?;
            let cmp = Expr::Binary(op, Box::new(left), Box::new(right.clone()));
            out = Some(match out {
                None => cmp,
                Some(o) => Expr::Binary(Op::And, Box::new(o), Box::new(cmp)),
            });
            left = right;
        }
        Some(out.unwrap_or(left))
    }

    /// The string, list and null predicates, each after white space.
    fn predicate(&mut self) -> Option<Expr> {
        let mut x = self.add()?;
        loop {
            if !self.sp() {
                break;
            }
            let op = if self.kw("STARTS") || self.kw("ENDS") {
                let starts = self.kw("STARTS");
                self.bump();
                if !self.sp() {
                    return None;
                }
                self.expect_kw("WITH")?;
                if starts { Op::StartsWith } else { Op::EndsWith }
            } else if self.kw("CONTAINS") {
                self.bump();
                Op::Contains
            } else if self.kw("IN") {
                self.bump();
                Op::In
            } else if self.kw("IS") {
                self.bump();
                if !self.sp() {
                    return None;
                }
                let not = self.kw("NOT");
                if not {
                    self.bump();
                    if !self.sp() {
                        return None;
                    }
                }
                self.expect_kw("NULL")?;
                x = Expr::IsNull(Box::new(x), not);
                continue;
            } else {
                break;
            };
            x = Expr::Binary(op, Box::new(x), Box::new(self.add()?));
        }
        Some(x)
    }

    fn add(&mut self) -> Option<Expr> {
        let mut x = self.mul()?;
        loop {
            let op = match self.peek().tok {
                Tok::Punct("+") => Op::Add,
                Tok::Punct("-") => Op::Sub,
                _ => break,
            };
            self.bump();
            x = Expr::Binary(op, Box::new(x), Box::new(self.mul()?));
        }
        Some(x)
    }

    fn mul(&mut self) -> Option<Expr> {
        let mut x = self.pow()?;
        loop {
            let op = match self.peek().tok {
                Tok::Punct("*") => Op::Mul,
                Tok::Punct("/") => Op::Div,
                Tok::Punct("%") => Op::Mod,
                _ => break,
            };
            self.bump();
            x = Expr::Binary(op, Box::new(x), Box::new(self.pow()?));
        }
        Some(x)
    }

    fn pow(&mut self) -> Option<Expr> {
        let mut x = self.unary()?;
        while self.eat("^") {
            x = Expr::Binary(Op::Pow, Box::new(x), Box::new(self.unary()?));
        }
        Some(x)
    }

    fn unary(&mut self) -> Option<Expr> {
        if self.eat("-") {
            return Some(Expr::Unary(Unary::Neg, Box::new(self.lookups()?)));
        }
        self.eat("+");
        self.lookups()
    }

    /// An atom, its list operators and property lookups, then its labels.
    fn lookups(&mut self) -> Option<Expr> {
        let mut x = self.atom()?;
        loop {
            if self.punct("[") {
                x = self.list_operator(x)?;
            } else if self.eat(".") {
                let key = self.schema_name()?;
                x = Expr::Prop(Box::new(x), key);
            } else {
                break;
            }
        }
        if self.punct(":") {
            x = Expr::HasLabels(Box::new(x), self.labels()?);
        }
        Some(x)
    }

    /// `[i]` or `[from..to]`, with no white space within.
    fn list_operator(&mut self, x: Expr) -> Option<Expr> {
        self.expect("[")?;
        let tight = |p: &Self| (!p.sp()).then_some(());
        tight(self)?;
        let from = if self.punct("..") { None } else { Some(self.expr()?) };
        tight(self)?;
        if self.eat("..") {
            tight(self)?;
            let to = if self.punct("]") { None } else { Some(self.expr()?) };
            tight(self)?;
            self.expect("]")?;
            return Some(Expr::Slice(Box::new(x), from.map(Box::new), to.map(Box::new)));
        }
        self.expect("]")?;
        Some(Expr::Index(Box::new(x), Box::new(from?)))
    }

    fn atom(&mut self) -> Option<Expr> {
        let t = self.peek().clone();
        match &t.tok {
            Tok::Str(s) => {
                self.bump();
                Some(Expr::Lit(Value::Str(s.clone())))
            }
            Tok::Int(v) => {
                self.bump();
                Some(Expr::Lit(Value::Num(*v as f64)))
            }
            Tok::Huge(text) => {
                self.bump();
                self.refuse(READ, t.start, format!("the integer {text}"));
                Some(Expr::Lit(Value::Null))
            }
            Tok::Real(text) => {
                self.bump();
                // a number past the largest float Go refuses; one below the smallest is 0
                let f: f64 = text.parse().unwrap_or(f64::INFINITY);
                if !f.is_finite() {
                    self.refuse(READ, t.start, format!("the number {text}"));
                    return Some(Expr::Lit(Value::Null));
                }
                Some(Expr::Lit(Value::Num(f)))
            }
            Tok::Punct("[") => {
                if self.comprehension() {
                    return Some(Expr::Lit(Value::Null));
                }
                self.bump();
                let mut items = Vec::new();
                if !self.eat("]") {
                    loop {
                        items.push(self.expr()?);
                        if self.eat("]") {
                            break;
                        }
                        self.expect(",")?;
                    }
                }
                Some(Expr::List(items))
            }
            Tok::Punct("{") => Some(Expr::Map(self.map_literal()?)),
            Tok::Punct("$") => {
                self.parameter()?;
                self.refuse(READ, t.start, "a parameter");
                Some(Expr::Lit(Value::Null))
            }
            Tok::Punct("(") => {
                let (at, refused) = (self.i, self.refused.len());
                if let Some(p) = self.chain().filter(|p| !p.rels.is_empty()) {
                    return Some(self.exists(t.start, vec![p], None));
                }
                self.i = at;
                self.refused.truncate(refused);
                self.bump();
                let e = self.expr()?;
                self.expect(")")?;
                Some(e)
            }
            Tok::Word(w) if w.eq_ignore_ascii_case("TRUE") || w.eq_ignore_ascii_case("FALSE") => {
                self.bump();
                Some(Expr::Lit(Value::Bool(w.eq_ignore_ascii_case("TRUE"))))
            }
            Tok::Word(w) if w.eq_ignore_ascii_case("NULL") => {
                self.bump();
                Some(Expr::Lit(Value::Null))
            }
            Tok::Word(w) if w.eq_ignore_ascii_case("CASE") => self.case(),
            Tok::Word(w) if w.eq_ignore_ascii_case("EXISTS") => {
                self.bump();
                self.expect("{")?;
                const QUERY: [&str; 10] = [
                    "MATCH", "OPTIONAL", "UNWIND", "CALL", "WITH", "RETURN", "CREATE", "MERGE", "SET", "DELETE",
                ];
                if QUERY.iter().any(|k| self.kw(k)) || self.kw("DETACH") || self.kw("REMOVE") {
                    // Go refuses it before it builds the query
                    let refused = self.refused.len();
                    self.regular_query()?;
                    self.refused.truncate(refused);
                    self.expect("}")?;
                    self.refuse(READ, t.start, "EXISTS over a query (write EXISTS { pattern WHERE … })");
                    return Some(Expr::Lit(Value::Null));
                }
                let patterns = self.patterns()?;
                let filter = self.filter()?;
                self.expect("}")?;
                Some(self.exists(t.start, patterns, filter))
            }
            Tok::Word(w) if w.eq_ignore_ascii_case("COUNT") => {
                self.bump();
                self.expect("(")?;
                if !self.eat("*") {
                    self.i -= 2; // count(x): a call
                    return self.call();
                }
                self.expect(")")?;
                Some(Expr::Call(Call {
                    name: "count".into(),
                    distinct: false,
                    star: true,
                    args: Vec::new(),
                }))
            }
            Tok::Word(w)
                if ["ALL", "ANY", "NONE", "SINGLE"]
                    .iter()
                    .any(|k| w.eq_ignore_ascii_case(k))
                    && matches!(self.next_tok().tok, Tok::Punct("(")) =>
            {
                if self.quantifier() {
                    return Some(Expr::Lit(Value::Null));
                }
                self.call()
            }
            Tok::Word(_) | Tok::Escaped(_) if matches!(self.next_tok().tok, Tok::Punct("(")) => self.call(),
            Tok::Word(_) | Tok::Escaped(_) if self.namespaced_call() => self.call(),
            Tok::Word(_) | Tok::Escaped(_) => Some(Expr::Var(self.variable()?)),
            _ => None,
        }
    }

    /// A function call: a known function or an aggregate, by its name in any case; Go refuses any other, and a name
    /// in a namespace (`a.b(x)`).
    fn call(&mut self) -> Option<Expr> {
        let start = self.peek().start;
        let mut namespaced = false;
        while matches!(self.next_tok().tok, Tok::Punct(".")) {
            if !self.symbolic_at(self.i) {
                return None;
            }
            self.bump();
            self.bump();
            namespaced = true;
        }
        if !self.symbolic_at(self.i) {
            return None;
        }
        let name = match &self.bump().tok {
            Tok::Word(w) | Tok::Escaped(w) => w.to_lowercase(),
            _ => return None,
        };
        if namespaced || (function(&name).is_none() && !AGGREGATES.contains(&name.as_str())) {
            let text = self.src[start..self.prev_end()].to_string();
            self.refuse(READ, start, format!("the function {text}"));
        }
        self.expect("(")?;
        let mut c = Call {
            name,
            distinct: false,
            star: false,
            args: Vec::new(),
        };
        if self.kw("DISTINCT") {
            self.bump();
            c.distinct = true;
        }
        if !self.eat(")") {
            loop {
                c.args.push(self.expr()?);
                if self.eat(")") {
                    break;
                }
                self.expect(",")?;
            }
        }
        Some(Expr::Call(c))
    }

    /// An EXISTS of patterns, at `at`: one holding another is refused (Go's `buildExists`).
    fn exists(&mut self, at: usize, patterns: Vec<Pattern>, filter: Option<Expr>) -> Expr {
        let x = Exists {
            patterns: Arc::new(patterns),
            filter,
        };
        let e = Expr::Exists(Arc::new(x));
        let mut nested = false;
        let mut first = true;
        e.walk(&mut |x| {
            if matches!(x, Expr::Exists(_)) && !std::mem::replace(&mut first, false) {
                nested = true;
            }
            !nested
        });
        if nested {
            self.refuse(READ, at, "an EXISTS or a pattern inside an EXISTS");
        }
        e
    }

    /// `$name` or `$1`, with no white space within.
    fn parameter(&mut self) -> Option<()> {
        self.expect("$")?;
        if self.sp() {
            return None;
        }
        let t = self.peek();
        let decimal = matches!(t.tok, Tok::Int(_)) && self.src[t.start..t.end].bytes().all(|c| c.is_ascii_digit());
        if !decimal && !self.symbolic_at(self.i) {
            return None;
        }
        self.bump();
        Some(())
    }

    /// Whether a name in a namespace and a parenthesis start here (`a.b(`), a function named so: never a property.
    fn namespaced_call(&self) -> bool {
        let mut j = self.i;
        let mut dots = 0;
        while self.symbolic_at(j)
            && matches!(self.t.get(j + 1).map(|t| &t.tok), Some(Tok::Punct(".")))
            && self.t.get(j + 2).is_some_and(|t| !t.sp)
            && !self.t[j + 1].sp
        {
            j += 2;
            dots += 1;
        }
        dots > 0 && self.symbolic_at(j) && matches!(self.t.get(j + 1).map(|t| &t.tok), Some(Tok::Punct("(")))
    }

    /// `variable SP IN SP expression (SP? WHERE SP expression)?`; whether there is a WHERE.
    fn filter_expression(&mut self) -> Option<bool> {
        self.variable()?;
        if !self.sp() || !self.kw("IN") {
            return None;
        }
        self.bump();
        if !self.sp() {
            return None;
        }
        self.expr()?;
        Some(self.filter()?.is_some())
    }

    /// A quantifier (`all(x IN l WHERE …)`), which Go refuses: whether one is read; else nothing is.
    fn quantifier(&mut self) -> bool {
        let (at, refused) = (self.i, self.refused.len());
        let start = self.peek().start;
        self.bump();
        if self.eat("(") && self.filter_expression().is_some() && self.eat(")") {
            self.refused.truncate(refused);
            self.refuse(READ, start, "a quantifier (all, any, none, single)");
            return true;
        }
        self.i = at;
        self.refused.truncate(refused);
        false
    }

    /// A list or pattern comprehension (`[x IN l WHERE … | …]`, `[(n)-->(m) | m.key]`), which Go refuses: whether one
    /// is read; else nothing is. A list of the grammar first: `[x IN l]` is a list.
    fn comprehension(&mut self) -> bool {
        let (at, refused) = (self.i, self.refused.len());
        let start = self.peek().start;
        self.bump();
        let read = |p: &mut Self| -> Option<()> {
            if p.punct("(") || matches!(p.next_tok().tok, Tok::Punct("=")) {
                if !p.punct("(") {
                    p.variable()?;
                    p.expect("=")?;
                }
                p.chain().filter(|c| !c.rels.is_empty())?;
                p.filter()?;
                p.expect("|")?;
                p.expr()?;
            } else {
                let filtered = p.filter_expression()?;
                let mapped = p.eat("|");
                if mapped {
                    p.expr()?;
                }
                if !filtered && !mapped {
                    return None;
                }
            }
            p.expect("]")
        };
        if read(self).is_some() {
            self.refused.truncate(refused);
            self.refuse(READ, start, "a list comprehension");
            return true;
        }
        self.i = at;
        self.refused.truncate(refused);
        false
    }

    fn case(&mut self) -> Option<Expr> {
        self.expect_kw("CASE")?;
        let subject = if self.kw("WHEN") {
            None
        } else {
            Some(Box::new(self.expr()?))
        };
        let mut c = Case {
            subject,
            whens: Vec::new(),
            thens: Vec::new(),
            otherwise: None,
        };
        while self.kw("WHEN") {
            self.bump();
            c.whens.push(self.expr()?);
            self.expect_kw("THEN")?;
            c.thens.push(self.expr()?);
        }
        if c.whens.is_empty() {
            return None;
        }
        if self.kw("ELSE") {
            self.bump();
            c.otherwise = Some(Box::new(self.expr()?));
        }
        self.expect_kw("END")?;
        Some(Expr::Case(c))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_the_subset() {
        let q = parse("MATCH (n:`alm@Requirement` {key: 'R-1'})-[r:refines*1..2]->(m) WHERE n.x = 1 AND m.y <> 'a' RETURN n.key AS k, count(*), m ORDER BY k DESC SKIP 1 LIMIT 2").unwrap();
        assert_eq!(q.matches.len(), 1);
        let p = &q.matches[0].patterns[0];
        assert_eq!(p.nodes[0].labels, vec!["alm@Requirement".to_string()]);
        assert_eq!(p.rels[0].range, Some(Range { min: 1, max: 2 }));
        assert_eq!(p.rels[0].dir, Direction::Right);
        assert_eq!(q.ret.items[0].name, "k");
        assert_eq!(q.ret.items[1].name, "count(*)");
        assert_eq!(q.ret.items[2].name, "m");
        assert!(q.ret.order[0].desc);
    }

    #[test]
    fn refuses_what_the_grammar_or_the_subset_refuses() {
        for src in [
            "MATCH (n) RETURN n ORDER BY n.a , n.b",
            "MATCH (n) WITH n RETURN n",
            "MATCH (n) RETURN x[ 1]",
            "MATCH (end) RETURN end",
            "MATCH (n) RETURNn",
            "MATCH (n) WHERE n.a=1 OR(n.b) RETURN n",
            "MATCH (n) RETURN foo(n)",
            "MATCH (n) RETURN $p",
            "MATCH (n) RETURN n LIMIT",
            "MATCH (n) WHERE EXISTS { (n)-->(m) WHERE EXISTS { (m)-->() } } RETURN n",
            "CREATE (n) RETURN n",
        ] {
            assert!(parse(src).is_none(), "{src}");
        }
        assert!(
            parse("MATCH (n) WHERE (n)-->(:B) AND NOT n.x IS NULL RETURN n.y STARTS WITH 'a', [1, 2][0..1]").is_some()
        );
    }

    #[test]
    fn refuses_in_go_words_what_it_reads_whole() {
        const RO: &str = "is not supported: the query language is read-only MATCH … RETURN (ADR 0160)";
        for (src, want) in [
            (
                "MATCH (n:dv@Thing) RETURN $p",
                "1:12: token recognition error at: '@'".to_string(),
            ),
            ("RETURN 1", format!("1:1: a query without MATCH {RO}")),
            ("\n  RETURN 1", format!("2:3: a query without MATCH {RO}")),
            ("UNWIND [1] AS x MATCH (n) RETURN n", format!("1:1: UNWIND {RO}")),
            ("MATCH (n) SET n.x = $p RETURN n", format!("1:11: a write (SET) {RO}")),
            (
                "MATCH (n) CREATE(m) RETURN m",
                format!("1:11: a write (CREATE(M)) {RO}"),
            ),
            ("MATCH (n) DETACH DELETE n", format!("1:11: a write (DETACH) {RO}")),
            (
                "MATCH (n) CREATE (m) WITH m MATCH (o) RETURN o",
                format!("1:22: WITH {RO}"),
            ),
            ("MATCH (n) RETURN n UNION RETURN 1", format!("1:20: UNION {RO}")),
            (
                "MATCH (n) RETURN [x IN [1] | $p]",
                format!("1:18: a list comprehension {RO}"),
            ),
            (
                "MATCH (n) RETURN [(n)-->(m) | m]",
                format!("1:18: a list comprehension {RO}"),
            ),
            (
                "MATCH (n) RETURN single (x IN [1])",
                format!("1:18: a quantifier (all, any, none, single) {RO}"),
            ),
            ("MATCH (n) RETURN any(x)", format!("1:18: the function any {RO}")),
            ("MATCH (n) RETURN n.key (1)", format!("1:18: the function n.key {RO}")),
            (
                "MATCH (n {k: $p}) WHERE foo(n) RETURN n",
                format!("1:14: a parameter {RO}"),
            ),
        ] {
            assert_eq!(read(src).err().map(|e| e.to_string()), Some(want), "{src}");
        }
        // what the grammar refuses, in this parser's words at the furthest token read
        for (src, want) in [
            (
                "MATCH (n:dv@Thing RETURN n",
                "1:12: token recognition error at: '@'".to_string(),
            ),
            ("WITH 1 AS x", "1:12: the query ends where more is expected".into()),
            (
                "MATCH (n) CREATE (m) MATCH (o) RETURN o",
                "1:22: unexpected 'MATCH'".into(),
            ),
            (
                "MATCH (n) RETURN [(n)-->(m) WHERE m.a = 1]",
                "1:42: unexpected ']'".into(),
            ),
            ("MATCH (n) RETURN n €", "1:20: token recognition error at: '€'".into()),
            (
                "MATCH (n) WHERE EXISTS { (n)-->(m) WHERE EXISTS { (m)-->() } } RETURN n",
                format!("1:17: an EXISTS or a pattern inside an EXISTS {RO}"),
            ),
            (
                "MATCH (n) WHERE EXISTS { MATCH (n) RETURN n } RETURN n",
                format!("1:17: EXISTS over a query (write EXISTS {{ pattern WHERE … }}) {RO}"),
            ),
            (
                "MATCH (n) RETURN 99999999999999999999",
                format!("1:18: the integer 99999999999999999999 {RO}"),
            ),
        ] {
            assert_eq!(read(src).err().map(|e| e.to_string()), Some(want), "{src}");
        }
        assert!(read("MATCH (é)\u{2014}\u{2014}\u{27e9}(a$b) RETURN é, a$b, 1e-400").is_ok());
        assert!(read("MATCH (n) RETURN [x IN [1]], [(n)-->(m)]").is_ok());
    }
}
