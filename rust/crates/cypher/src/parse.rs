//! A query of the subset read into its tree (Go's `lib/cypher/parse.go`), by descent over the rules of the openCypher
//! grammar the subset keeps, with the grammar's white space rules. A query this parser does not read is not answered
//! here: the Go service, whose parser is generated from the grammar, answers it with the grammar's or the subset's
//! own words, so nothing the grammar refuses may be read here.
use std::sync::Arc;

use crate::ast::{
    AGGREGATES, Call, Case, Direction, Exists, Expr, Item, Match, NodePattern, Op, Pattern, Query, Range, RelPattern,
    Return, SortItem, Unary,
};
use crate::eval::{Value, function};
use crate::lex::{Tok, Token, tokens};

/// The longest query Parse reads (Go's `MaxQueryBytes`).
pub const MAX_QUERY_BYTES: usize = 16 << 10;

/// Reads a query of the subset; None for a query not read here.
pub fn parse(src: &str) -> Option<Query> {
    if src.len() > MAX_QUERY_BYTES || src.trim().is_empty() {
        return None;
    }
    let mut p = Parser {
        src,
        t: tokens(src)?,
        i: 0,
    };
    let q = p.query()?;
    Some(q)
}

struct Parser<'s> {
    src: &'s str,
    t: Vec<Token>,
    i: usize,
}

impl Parser<'_> {
    fn peek(&self) -> &Token {
        &self.t[self.i]
    }

    fn next_tok(&self) -> &Token {
        &self.t[(self.i + 1).min(self.t.len() - 1)]
    }

    fn bump(&mut self) -> &Token {
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

    fn query(&mut self) -> Option<Query> {
        let mut matches = Vec::new();
        loop {
            let optional = if self.kw("OPTIONAL") {
                self.bump();
                if !self.sp() {
                    return None;
                }
                self.expect_kw("MATCH")?;
                true
            } else if self.kw("MATCH") {
                self.bump();
                false
            } else {
                break;
            };
            let patterns = self.patterns()?;
            let filter = self.filter()?;
            matches.push(Match {
                optional,
                patterns: Arc::new(patterns),
                filter,
            });
        }
        if matches.is_empty() {
            return None;
        }
        self.expect_kw("RETURN")?;
        let ret = self.projection()?;
        self.eat(";");
        matches!(self.peek().tok, Tok::Eof).then_some(Query { matches, ret })
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
        while self.punct("<") || self.punct("-") {
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

    fn rel_pattern(&mut self) -> Option<RelPattern> {
        let left = self.eat("<");
        self.expect("-")?;
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
            }
            self.expect("]")?;
        }
        self.expect("-")?;
        let right = self.eat(">");
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
            match p.peek().tok {
                Tok::Int(v) => {
                    p.bump();
                    if !(0..=1 << 20).contains(&v) {
                        return None;
                    }
                    Some(Some(v as usize))
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
            Tok::Real(text) => {
                self.bump();
                let f: f64 = text.parse().ok()?;
                // a number Go reads out of range: its words
                let zero = text
                    .split(['e', 'E'])
                    .next()
                    .is_some_and(|m| m.bytes().all(|c| c == b'0' || c == b'.'));
                if !f.is_finite() || (f == 0.0 && !zero) {
                    return None;
                }
                Some(Expr::Lit(Value::Num(f)))
            }
            Tok::Punct("[") => {
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
            Tok::Punct("(") => {
                let at = self.i;
                if let Some(p) = self.chain().filter(|p| !p.rels.is_empty()) {
                    return exists(vec![p], None);
                }
                self.i = at;
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
                let patterns = self.patterns()?;
                let filter = self.filter()?;
                self.expect("}")?;
                exists(patterns, filter)
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
            Tok::Word(_) | Tok::Escaped(_) if matches!(self.next_tok().tok, Tok::Punct("(")) => self.call(),
            Tok::Word(_) | Tok::Escaped(_) => Some(Expr::Var(self.variable()?)),
            _ => None,
        }
    }

    /// A function call: a known function or an aggregate, by its name in any case.
    fn call(&mut self) -> Option<Expr> {
        let name = match &self.bump().tok {
            Tok::Word(w) | Tok::Escaped(w) => w.to_lowercase(),
            _ => return None,
        };
        if function(&name).is_none() && !AGGREGATES.contains(&name.as_str()) {
            return None;
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

/// An EXISTS of patterns, holding no other.
fn exists(patterns: Vec<Pattern>, filter: Option<Expr>) -> Option<Expr> {
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
    (!nested).then_some(e)
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
}
