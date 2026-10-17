//! A subset of Go's text/template, enough for the prompts of the methodologies (`{{ .Change.Intent }}`, `range`,
//! `if` / `else if` / `else`, `with`, pipelines, variables and the functions the engine gives them), executed with
//! `missingkey=zero`: a key missing from a map prints `<no value>`.
//!
//! Data is a [`TV`]: Go structs keep their Go field names and their JSON form (for the `json` function), maps keep
//! their keys. A deviation from Go: a field a struct does not have is an error only when it is printed or walked
//! into; as a condition it is false.

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::rc::Rc;

use serde_json::Value;

/// A value of the template data.
#[derive(Clone, Debug)]
pub enum TV {
    /// Go's nil.
    Nil,
    /// The zero value of a missing map key (printed `<no value>`).
    NoValue,
    Bool(bool),
    Int(i64),
    Float(f64),
    Str(String),
    List(Rc<Vec<TV>>),
    Map(Rc<BTreeMap<String, TV>>),
    Struct(Rc<Struct>),
}

/// A Go struct: its fields by Go name and its JSON form (what `json` writes).
#[derive(Debug)]
pub struct Struct {
    pub name: &'static str,
    pub fields: Vec<(&'static str, TV)>,
    pub json: Value,
}

impl TV {
    /// The value of a JSON value (a map keeps its keys).
    pub fn from_json(v: &Value) -> TV {
        match v {
            Value::Null => TV::Nil,
            Value::Bool(b) => TV::Bool(*b),
            Value::Number(n) => match n.as_i64() {
                Some(i) => TV::Int(i),
                None => TV::Float(n.as_f64().unwrap_or(0.0)),
            },
            Value::String(s) => TV::Str(s.clone()),
            Value::Array(l) => TV::List(Rc::new(l.iter().map(TV::from_json).collect())),
            Value::Object(m) => TV::Map(Rc::new(
                m.iter()
                    .map(|(k, v)| (k.clone(), TV::from_json(v)))
                    .collect(),
            )),
        }
    }

    /// A struct of its fields and JSON form.
    pub fn structure(name: &'static str, fields: Vec<(&'static str, TV)>, json: Value) -> TV {
        TV::Struct(Rc::new(Struct { name, fields, json }))
    }

    pub fn str(s: impl Into<String>) -> TV {
        TV::Str(s.into())
    }

    pub fn list(l: Vec<TV>) -> TV {
        TV::List(Rc::new(l))
    }

    /// The JSON form of the value.
    pub fn to_json(&self) -> Value {
        match self {
            TV::Nil | TV::NoValue => Value::Null,
            TV::Bool(b) => Value::Bool(*b),
            TV::Int(i) => Value::from(*i),
            TV::Float(f) => serde_json::Number::from_f64(*f)
                .map(Value::Number)
                .unwrap_or(Value::Null),
            TV::Str(s) => Value::String(s.clone()),
            TV::List(l) => Value::Array(l.iter().map(TV::to_json).collect()),
            TV::Map(m) => Value::Object(m.iter().map(|(k, v)| (k.clone(), v.to_json())).collect()),
            TV::Struct(s) => s.json.clone(),
        }
    }

    /// Go's truth of a value (`if`, `with`, `and`, `or`, `not`).
    pub fn truth(&self) -> bool {
        match self {
            TV::Nil | TV::NoValue => false,
            TV::Bool(b) => *b,
            TV::Int(i) => *i != 0,
            TV::Float(f) => *f != 0.0,
            TV::Str(s) => !s.is_empty(),
            TV::List(l) => !l.is_empty(),
            TV::Map(m) => !m.is_empty(),
            TV::Struct(_) => true,
        }
    }

    fn kind(&self) -> &'static str {
        match self {
            TV::Nil | TV::NoValue => "nil",
            TV::Bool(_) => "bool",
            TV::Int(_) => "int",
            TV::Float(_) => "float64",
            TV::Str(_) => "string",
            TV::List(_) => "slice",
            TV::Map(_) => "map",
            TV::Struct(s) => s.name,
        }
    }

    /// Go's `%v`.
    pub fn print(&self) -> String {
        let mut out = String::new();
        self.write(&mut out, true);
        out
    }

    fn write(&self, out: &mut String, top: bool) {
        match self {
            TV::Nil => out.push_str("<nil>"),
            TV::NoValue => out.push_str(if top { "<no value>" } else { "<nil>" }),
            TV::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
            TV::Int(i) => {
                let _ = write!(out, "{i}");
            }
            TV::Float(f) => out.push_str(&go_float(*f)),
            TV::Str(s) => out.push_str(s),
            TV::List(l) => {
                out.push('[');
                for (i, v) in l.iter().enumerate() {
                    if i > 0 {
                        out.push(' ');
                    }
                    v.write(out, false);
                }
                out.push(']');
            }
            TV::Map(m) => {
                out.push_str("map[");
                for (i, (k, v)) in m.iter().enumerate() {
                    if i > 0 {
                        out.push(' ');
                    }
                    out.push_str(k);
                    out.push(':');
                    v.write(out, false);
                }
                out.push(']');
            }
            TV::Struct(s) => {
                out.push('{');
                for (i, (_, v)) in s.fields.iter().enumerate() {
                    if i > 0 {
                        out.push(' ');
                    }
                    v.write(out, false);
                }
                out.push('}');
            }
        }
    }
}

/// Go's `%v` of a float64 (strconv 'g' with the shortest precision, exponent below 1e-4 and from 1e21).
pub fn go_float(f: f64) -> String {
    if f.is_nan() {
        return "NaN".into();
    }
    if f.is_infinite() {
        return if f > 0.0 {
            "+Inf".into()
        } else {
            "-Inf".into()
        };
    }
    let a = f.abs();
    if a != 0.0 && !(1e-4..1e21).contains(&a) {
        let s = format!("{f:e}");
        let (m, e) = s.split_once('e').unwrap_or((&s, "0"));
        let e: i32 = e.parse().unwrap_or(0);
        let sign = if e < 0 { '-' } else { '+' };
        return format!("{m}e{sign}{:02}", e.abs());
    }
    format!("{f}")
}

/// A function of the template: its arguments evaluated, its result or an error.
pub type Func = Box<dyn Fn(&[TV]) -> Result<TV, String>>;

/// The functions a template can call besides the built-in ones.
#[derive(Default)]
pub struct Funcs(pub BTreeMap<String, Func>);

impl Funcs {
    pub fn add(&mut self, name: &str, f: impl Fn(&[TV]) -> Result<TV, String> + 'static) {
        self.0.insert(name.to_string(), Box::new(f));
    }
}

// ---- parsing -----------------------------------------------------------------------------------------------------

#[derive(Debug, Clone)]
enum Node {
    Text(String),
    Action(Pipe),
    If(Vec<(Pipe, Vec<Node>)>, Vec<Node>),
    Range(Pipe, Vec<Node>, Vec<Node>),
    With(Vec<(Pipe, Vec<Node>)>, Vec<Node>),
}

#[derive(Debug, Clone)]
struct Pipe {
    decl: Vec<String>,
    assign: bool,
    cmds: Vec<Vec<Arg>>,
}

#[derive(Debug, Clone)]
enum Arg {
    Dot(Vec<String>),
    Var(String, Vec<String>),
    Ident(String),
    Str(String),
    Int(i64),
    Float(f64),
    Bool(bool),
    Nil,
    Sub(Box<Pipe>, Vec<String>),
}

/// A parsed template.
pub struct Template {
    name: String,
    nodes: Vec<Node>,
}

enum Item {
    Text(String),
    Action(String),
}

fn lex(src: &str) -> Result<Vec<Item>, String> {
    let mut items = Vec::new();
    let mut rest = src;
    let mut trim_next = false;
    while !rest.is_empty() {
        match rest.find("{{") {
            None => {
                let t = if trim_next { rest.trim_start() } else { rest };
                if !t.is_empty() {
                    items.push(Item::Text(t.to_string()));
                }
                break;
            }
            Some(i) => {
                let mut text = &rest[..i];
                if trim_next {
                    text = text.trim_start();
                }
                let after = &rest[i + 2..];
                let trim_left =
                    after.starts_with("- ") || after.starts_with("-\t") || after.starts_with("-\n");
                if trim_left {
                    text = text.trim_end();
                }
                if !text.is_empty() {
                    items.push(Item::Text(text.to_string()));
                }
                let body_start = if trim_left { &after[1..] } else { after };
                let end = find_close(body_start).ok_or("unclosed action")?;
                let mut body = &body_start[..end];
                trim_next = false;
                if body.ends_with(" -") || body.ends_with("\t-") || body.ends_with("\n-") {
                    body = &body[..body.len() - 1];
                    trim_next = true;
                }
                rest = &body_start[end + 2..];
                let body = body.trim();
                if body.starts_with("/*") {
                    continue; // a comment
                }
                items.push(Item::Action(body.to_string()));
            }
        }
    }
    Ok(items)
}

/// find_close finds the "}}" closing an action, outside of string literals.
fn find_close(s: &str) -> Option<usize> {
    let b = s.as_bytes();
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'"' => {
                i += 1;
                while i < b.len() && b[i] != b'"' {
                    if b[i] == b'\\' {
                        i += 1;
                    }
                    i += 1;
                }
            }
            b'`' => {
                i += 1;
                while i < b.len() && b[i] != b'`' {
                    i += 1;
                }
            }
            b'}' if i + 1 < b.len() && b[i + 1] == b'}' => return Some(i),
            _ => {}
        }
        i += 1;
    }
    None
}

#[derive(Debug, Clone, PartialEq)]
enum Tok {
    Field(String),
    Dot,
    Var(String),
    Ident(String),
    Str(String),
    Num(String),
    LParen,
    RParen,
    Pipe,
    Decl,
    Assign,
    Comma,
}

fn tokens(s: &str) -> Result<Vec<Tok>, String> {
    let c: Vec<char> = s.chars().collect();
    let mut out = Vec::new();
    let mut i = 0;
    let ident = |i: &mut usize| {
        let st = *i;
        while *i < c.len() && (c[*i].is_alphanumeric() || c[*i] == '_') {
            *i += 1;
        }
        c[st..*i].iter().collect::<String>()
    };
    while i < c.len() {
        let ch = c[i];
        match ch {
            ' ' | '\t' | '\n' | '\r' => i += 1,
            '(' => {
                out.push(Tok::LParen);
                i += 1
            }
            ')' => {
                out.push(Tok::RParen);
                i += 1
            }
            '|' => {
                out.push(Tok::Pipe);
                i += 1
            }
            ',' => {
                out.push(Tok::Comma);
                i += 1
            }
            ':' if i + 1 < c.len() && c[i + 1] == '=' => {
                out.push(Tok::Decl);
                i += 2
            }
            '=' => {
                out.push(Tok::Assign);
                i += 1
            }
            '.' => {
                i += 1;
                if i < c.len() && (c[i].is_alphabetic() || c[i] == '_') {
                    out.push(Tok::Field(ident(&mut i)));
                } else {
                    out.push(Tok::Dot);
                }
            }
            '$' => {
                i += 1;
                out.push(Tok::Var(format!("${}", ident(&mut i))));
            }
            '"' => {
                i += 1;
                let mut s = String::new();
                while i < c.len() && c[i] != '"' {
                    if c[i] == '\\' && i + 1 < c.len() {
                        i += 1;
                        s.push(match c[i] {
                            'n' => '\n',
                            't' => '\t',
                            'r' => '\r',
                            x => x,
                        });
                    } else {
                        s.push(c[i]);
                    }
                    i += 1;
                }
                if i >= c.len() {
                    return Err("unterminated quoted string".into());
                }
                i += 1;
                out.push(Tok::Str(s));
            }
            '`' => {
                i += 1;
                let st = i;
                while i < c.len() && c[i] != '`' {
                    i += 1;
                }
                out.push(Tok::Str(c[st..i].iter().collect()));
                i += 1;
            }
            x if x.is_ascii_digit()
                || ((x == '-' || x == '+') && i + 1 < c.len() && c[i + 1].is_ascii_digit()) =>
            {
                let st = i;
                i += 1;
                while i < c.len() && (c[i].is_ascii_alphanumeric() || c[i] == '.' || c[i] == '_') {
                    i += 1;
                }
                out.push(Tok::Num(c[st..i].iter().collect()));
            }
            x if x.is_alphabetic() || x == '_' => out.push(Tok::Ident(ident(&mut i))),
            x => return Err(format!("unexpected {x:?} in command")),
        }
    }
    Ok(out)
}

struct Parser {
    toks: Vec<Tok>,
    pos: usize,
}

impl Parser {
    fn peek(&self) -> Option<&Tok> {
        self.toks.get(self.pos)
    }

    fn pipe(&mut self) -> Result<Pipe, String> {
        let mut decl = Vec::new();
        let mut assign = false;
        // $x := / $i, $v := / $x =
        let save = self.pos;
        let mut vars = Vec::new();
        while let Some(Tok::Var(v)) = self.peek().cloned() {
            vars.push(v);
            self.pos += 1;
            if self.peek() == Some(&Tok::Comma) {
                self.pos += 1;
                continue;
            }
            break;
        }
        match self.peek() {
            Some(Tok::Decl) if !vars.is_empty() => {
                self.pos += 1;
                decl = vars;
            }
            Some(Tok::Assign) if vars.len() == 1 => {
                self.pos += 1;
                decl = vars;
                assign = true;
            }
            _ => self.pos = save,
        }
        let mut cmds = vec![self.command()?];
        while self.peek() == Some(&Tok::Pipe) {
            self.pos += 1;
            cmds.push(self.command()?);
        }
        Ok(Pipe { decl, assign, cmds })
    }

    fn command(&mut self) -> Result<Vec<Arg>, String> {
        let mut args = Vec::new();
        while let Some(t) = self.peek() {
            if matches!(t, Tok::Pipe | Tok::RParen) {
                break;
            }
            args.push(self.operand()?);
        }
        if args.is_empty() {
            return Err("missing value for command".into());
        }
        Ok(args)
    }

    fn fields(&mut self) -> Vec<String> {
        let mut out = Vec::new();
        while let Some(Tok::Field(f)) = self.peek().cloned() {
            out.push(f);
            self.pos += 1;
        }
        out
    }

    fn operand(&mut self) -> Result<Arg, String> {
        let t = self.peek().cloned().ok_or("unexpected end")?;
        self.pos += 1;
        Ok(match t {
            Tok::Dot => Arg::Dot(Vec::new()),
            Tok::Field(f) => {
                let mut path = vec![f];
                path.extend(self.fields());
                Arg::Dot(path)
            }
            Tok::Var(v) => Arg::Var(v, self.fields()),
            Tok::Ident(id) => match id.as_str() {
                "true" => Arg::Bool(true),
                "false" => Arg::Bool(false),
                "nil" => Arg::Nil,
                _ => Arg::Ident(id),
            },
            Tok::Str(s) => Arg::Str(s),
            Tok::Num(n) => {
                if let Ok(i) = n.parse::<i64>() {
                    Arg::Int(i)
                } else if let Ok(f) = n.parse::<f64>() {
                    Arg::Float(f)
                } else {
                    return Err(format!("bad number syntax: {n:?}"));
                }
            }
            Tok::LParen => {
                let p = self.pipe()?;
                if self.peek() != Some(&Tok::RParen) {
                    return Err("unclosed left paren".into());
                }
                self.pos += 1;
                Arg::Sub(Box::new(p), self.fields())
            }
            t => return Err(format!("unexpected {t:?} in operand")),
        })
    }
}

fn parse_pipe(s: &str) -> Result<Pipe, String> {
    let mut p = Parser {
        toks: tokens(s)?,
        pos: 0,
    };
    let pipe = p.pipe()?;
    if p.pos < p.toks.len() {
        return Err(format!("unexpected {:?} in command", p.toks[p.pos]));
    }
    Ok(pipe)
}

#[allow(clippy::enum_variant_names)]
enum End {
    End,
    Else(Option<(String, String)>),
    Eof,
}

fn parse_list(
    items: &mut std::iter::Peekable<std::vec::IntoIter<Item>>,
) -> Result<(Vec<Node>, End), String> {
    let mut nodes = Vec::new();
    while let Some(it) = items.next() {
        match it {
            Item::Text(t) => nodes.push(Node::Text(t)),
            Item::Action(a) => {
                let (word, rest) = match a.split_once(char::is_whitespace) {
                    Some((w, r)) => (w, r.trim()),
                    None => (a.as_str(), ""),
                };
                match word {
                    "end" => return Ok((nodes, End::End)),
                    "else" => {
                        if rest.is_empty() {
                            return Ok((nodes, End::Else(None)));
                        }
                        let (w2, r2) = rest.split_once(char::is_whitespace).unwrap_or((rest, ""));
                        return Ok((
                            nodes,
                            End::Else(Some((w2.to_string(), r2.trim().to_string()))),
                        ));
                    }
                    "if" | "with" => {
                        let mut branches = vec![];
                        let mut cond = parse_pipe(rest)?;
                        let otherwise;
                        loop {
                            let (body, end) = parse_list(items)?;
                            branches.push((cond, body));
                            match end {
                                End::End => {
                                    otherwise = Vec::new();
                                    break;
                                }
                                End::Else(None) => {
                                    let (body, end) = parse_list(items)?;
                                    if !matches!(end, End::End) {
                                        return Err(format!("expected end after else of {word}"));
                                    }
                                    otherwise = body;
                                    break;
                                }
                                End::Else(Some((w, r))) if w == word => cond = parse_pipe(&r)?,
                                End::Else(Some((w, _))) => {
                                    return Err(format!("unexpected else {w}"));
                                }
                                End::Eof => return Err(format!("unexpected EOF in {word}")),
                            }
                        }
                        nodes.push(if word == "if" {
                            Node::If(branches, otherwise)
                        } else {
                            Node::With(branches, otherwise)
                        });
                    }
                    "range" => {
                        let p = parse_pipe(rest)?;
                        let (body, end) = parse_list(items)?;
                        let otherwise = match end {
                            End::End => Vec::new(),
                            End::Else(None) => {
                                let (b, end) = parse_list(items)?;
                                if !matches!(end, End::End) {
                                    return Err("expected end after else of range".into());
                                }
                                b
                            }
                            _ => return Err("unexpected EOF in range".into()),
                        };
                        nodes.push(Node::Range(p, body, otherwise));
                    }
                    "define" | "template" | "block" => {
                        return Err(format!("{word} is not supported in a prompt"));
                    }
                    _ => nodes.push(Node::Action(parse_pipe(&a)?)),
                }
            }
        }
    }
    Ok((nodes, End::Eof))
}

impl Template {
    /// Parses a template.
    pub fn parse(name: &str, src: &str) -> Result<Template, String> {
        let wrap = |e: String| format!("template: {name}: {e}");
        let mut items = lex(src).map_err(wrap)?.into_iter().peekable();
        let (nodes, end) = parse_list(&mut items).map_err(wrap)?;
        match end {
            End::Eof => Ok(Template {
                name: name.to_string(),
                nodes,
            }),
            End::End => Err(wrap("unexpected {{end}}".into())),
            End::Else(_) => Err(wrap("unexpected {{else}}".into())),
        }
    }

    /// Executes the template on data with the functions given.
    pub fn execute(&self, data: &TV, funcs: &Funcs) -> Result<String, String> {
        let mut ex = Exec {
            funcs,
            vars: vec![("$".to_string(), data.clone())],
            out: String::new(),
        };
        ex.list(&self.nodes, data)
            .map_err(|e| format!("template: {}: {e}", self.name))?;
        Ok(ex.out)
    }
}

// ---- execution ---------------------------------------------------------------------------------------------------

struct Exec<'a> {
    funcs: &'a Funcs,
    vars: Vec<(String, TV)>,
    out: String,
}

fn field(v: &TV, name: &str) -> Result<TV, String> {
    match v {
        TV::Struct(s) => s
            .fields
            .iter()
            .find(|(n, _)| *n == name)
            .map(|(_, v)| v.clone())
            .ok_or_else(|| format!("can't evaluate field {name} in type {}", s.name)),
        TV::Map(m) => Ok(m.get(name).cloned().unwrap_or(TV::NoValue)),
        TV::Nil | TV::NoValue => Ok(TV::NoValue),
        v => Err(format!("can't evaluate field {name} in type {}", v.kind())),
    }
}

fn fields(mut v: TV, path: &[String]) -> Result<TV, String> {
    for f in path {
        v = field(&v, f)?;
    }
    Ok(v)
}

fn num(v: &TV) -> Option<f64> {
    match v {
        TV::Int(i) => Some(*i as f64),
        TV::Float(f) => Some(*f),
        _ => None,
    }
}

fn equal(a: &TV, b: &TV) -> Result<bool, String> {
    Ok(match (a, b) {
        (TV::Str(x), TV::Str(y)) => x == y,
        (TV::Bool(x), TV::Bool(y)) => x == y,
        (TV::Int(x), TV::Int(y)) => x == y,
        (x, y) if num(x).is_some() && num(y).is_some() => num(x) == num(y),
        (TV::Nil | TV::NoValue, TV::Nil | TV::NoValue) => true,
        (TV::Nil | TV::NoValue, _) | (_, TV::Nil | TV::NoValue) => false,
        (x, y) => {
            return Err(format!(
                "incompatible types for comparison: {} and {}",
                x.kind(),
                y.kind()
            ));
        }
    })
}

fn less(a: &TV, b: &TV) -> Result<bool, String> {
    match (a, b) {
        (TV::Str(x), TV::Str(y)) => Ok(x < y),
        (x, y) if num(x).is_some() && num(y).is_some() => Ok(num(x) < num(y)),
        (x, y) => Err(format!(
            "incompatible types for comparison: {} and {}",
            x.kind(),
            y.kind()
        )),
    }
}

fn index(v: &TV, keys: &[TV]) -> Result<TV, String> {
    let mut v = v.clone();
    for k in keys {
        v = match (&v, k) {
            (TV::Map(m), TV::Str(s)) => m.get(s).cloned().unwrap_or(TV::NoValue),
            (TV::List(l), k) if num(k).is_some() => {
                let i = num(k).unwrap() as i64;
                if i < 0 || i as usize >= l.len() {
                    return Err(format!("error calling index: index out of range: {i}"));
                }
                l[i as usize].clone()
            }
            (TV::Nil | TV::NoValue, _) => TV::NoValue,
            (TV::Struct(_), _) => {
                return Err("error calling index: can't index item of type struct".into());
            }
            (v, _) => {
                return Err(format!(
                    "error calling index: can't index item of type {}",
                    v.kind()
                ));
            }
        };
    }
    Ok(v)
}

/// Go's fmt.Sprintf for the verbs prompts use (%s %v %d %q %f %t %%).
pub fn sprintf(format: &str, args: &[TV]) -> String {
    let mut out = String::new();
    let mut it = format.chars().peekable();
    let mut n = 0;
    while let Some(c) = it.next() {
        if c != '%' {
            out.push(c);
            continue;
        }
        let mut spec = String::new();
        while let Some(&d) = it.peek() {
            if d.is_ascii_digit() || d == '.' || d == '-' || d == '+' || d == '#' {
                spec.push(d);
                it.next();
            } else {
                break;
            }
        }
        let Some(verb) = it.next() else {
            out.push_str("%!(NOVERB)");
            break;
        };
        if verb == '%' {
            out.push('%');
            continue;
        }
        let Some(a) = args.get(n) else {
            let _ = write!(out, "%!{verb}(MISSING)");
            continue;
        };
        n += 1;
        match verb {
            'q' => out.push_str(&serde_json::to_string(&a.print()).unwrap_or_default()),
            'd' => match a {
                TV::Int(i) => {
                    let _ = write!(out, "{i}");
                }
                a => {
                    let _ = write!(out, "%!d({}={})", a.kind(), a.print());
                }
            },
            'f' => {
                let prec = spec
                    .split_once('.')
                    .and_then(|(_, p)| p.parse::<usize>().ok())
                    .unwrap_or(6);
                match num(a) {
                    Some(f) => {
                        let _ = write!(out, "{f:.prec$}");
                    }
                    None => {
                        let _ = write!(out, "%!f({}={})", a.kind(), a.print());
                    }
                }
            }
            _ => out.push_str(&a.print()),
        }
    }
    if n < args.len() {
        out.push_str("%!(EXTRA ");
        let extra: Vec<String> = args[n..]
            .iter()
            .map(|a| format!("{}={}", a.kind(), a.print()))
            .collect();
        out.push_str(&extra.join(", "));
        out.push(')');
    }
    out
}

impl Exec<'_> {
    fn var(&self, name: &str) -> Result<TV, String> {
        self.vars
            .iter()
            .rev()
            .find(|(n, _)| n == name)
            .map(|(_, v)| v.clone())
            .ok_or_else(|| format!("undefined variable: {name}"))
    }

    fn list(&mut self, nodes: &[Node], dot: &TV) -> Result<(), String> {
        for n in nodes {
            self.node(n, dot)?;
        }
        Ok(())
    }

    fn scoped(&mut self, nodes: &[Node], dot: &TV) -> Result<(), String> {
        let mark = self.vars.len();
        let r = self.list(nodes, dot);
        self.vars.truncate(mark);
        r
    }

    fn node(&mut self, n: &Node, dot: &TV) -> Result<(), String> {
        match n {
            Node::Text(t) => self.out.push_str(t),
            Node::Action(p) => {
                let v = self.pipe(p, dot)?;
                if p.decl.is_empty() {
                    if let TV::Struct(_) = v {
                        // printing a struct is allowed, Go writes its fields
                    }
                    self.out.push_str(&v.print());
                }
            }
            Node::If(branches, otherwise) => {
                let mark = self.vars.len();
                for (cond, body) in branches {
                    if self.cond(cond, dot)?.truth() {
                        let r = self.list(body, dot);
                        self.vars.truncate(mark);
                        return r;
                    }
                }
                let r = self.list(otherwise, dot);
                self.vars.truncate(mark);
                return r;
            }
            Node::With(branches, otherwise) => {
                let mark = self.vars.len();
                for (cond, body) in branches {
                    let v = self.cond(cond, dot)?;
                    if v.truth() {
                        let r = self.list(body, &v);
                        self.vars.truncate(mark);
                        return r;
                    }
                }
                let r = self.list(otherwise, dot);
                self.vars.truncate(mark);
                return r;
            }
            Node::Range(p, body, otherwise) => {
                let mark = self.vars.len();
                let v = self.cond(
                    &Pipe {
                        decl: vec![],
                        ..p.clone()
                    },
                    dot,
                )?;
                let elems: Vec<(TV, TV)> = match &v {
                    TV::List(l) => l
                        .iter()
                        .enumerate()
                        .map(|(i, x)| (TV::Int(i as i64), x.clone()))
                        .collect(),
                    TV::Map(m) => m
                        .iter()
                        .map(|(k, x)| (TV::Str(k.clone()), x.clone()))
                        .collect(),
                    TV::Nil | TV::NoValue => Vec::new(),
                    TV::Int(n) => (0..*n).map(|i| (TV::Int(i), TV::Int(i))).collect(),
                    v => return Err(format!("range can't iterate over {}", v.print())),
                };
                if elems.is_empty() {
                    let r = self.scoped(otherwise, dot);
                    self.vars.truncate(mark);
                    return r;
                }
                for (k, x) in elems {
                    let inner = self.vars.len();
                    match p.decl.len() {
                        0 => {}
                        1 => self.vars.push((p.decl[0].clone(), x.clone())),
                        _ => {
                            self.vars.push((p.decl[0].clone(), k));
                            self.vars.push((p.decl[1].clone(), x.clone()));
                        }
                    }
                    let r = self.list(body, &x);
                    self.vars.truncate(inner);
                    r?;
                }
                self.vars.truncate(mark);
            }
        }
        Ok(())
    }

    /// cond evaluates the pipeline of if / with / range, declaring its variables in the current scope.
    fn cond(&mut self, p: &Pipe, dot: &TV) -> Result<TV, String> {
        self.pipe(p, dot)
    }

    fn pipe(&mut self, p: &Pipe, dot: &TV) -> Result<TV, String> {
        let mut prev: Option<TV> = None;
        for cmd in &p.cmds {
            prev = Some(self.command(cmd, dot, prev)?);
        }
        let v = prev.unwrap_or(TV::Nil);
        if !p.decl.is_empty() {
            if p.assign {
                let name = &p.decl[0];
                match self.vars.iter_mut().rev().find(|(n, _)| n == name) {
                    Some(slot) => slot.1 = v.clone(),
                    None => return Err(format!("undefined variable: {name}")),
                }
            } else {
                self.vars.push((p.decl[0].clone(), v.clone()));
            }
        }
        Ok(v)
    }

    fn arg(&mut self, a: &Arg, dot: &TV) -> Result<TV, String> {
        match a {
            Arg::Dot(path) => fields(dot.clone(), path),
            Arg::Var(name, path) => fields(self.var(name)?, path),
            Arg::Str(s) => Ok(TV::Str(s.clone())),
            Arg::Int(i) => Ok(TV::Int(*i)),
            Arg::Float(f) => Ok(TV::Float(*f)),
            Arg::Bool(b) => Ok(TV::Bool(*b)),
            Arg::Nil => Ok(TV::Nil),
            Arg::Sub(p, path) => {
                let v = self.pipe(p, dot)?;
                fields(v, path)
            }
            Arg::Ident(f) => self.call(f, &[], dot, None),
        }
    }

    fn command(&mut self, cmd: &[Arg], dot: &TV, prev: Option<TV>) -> Result<TV, String> {
        if let Arg::Ident(f) = &cmd[0] {
            return self.call(f, &cmd[1..], dot, prev);
        }
        if cmd.len() > 1 || prev.is_some() {
            return Err("can't give argument to non-function".into());
        }
        self.arg(&cmd[0], dot)
    }

    fn call(&mut self, f: &str, args: &[Arg], dot: &TV, prev: Option<TV>) -> Result<TV, String> {
        // and / or evaluate their arguments only as far as needed
        if f == "and" || f == "or" {
            let mut all: Vec<&Arg> = args.iter().collect();
            let mut last = TV::Nil;
            let tail = prev;
            if all.is_empty() && tail.is_none() {
                return Err(format!(
                    "wrong number of args for {f}: want at least 1 got 0"
                ));
            }
            for a in all.drain(..) {
                last = self.arg(a, dot)?;
                if (f == "and") != last.truth() {
                    return Ok(last);
                }
            }
            if let Some(t) = tail {
                last = t;
            }
            return Ok(last);
        }
        let mut vals = Vec::with_capacity(args.len() + 1);
        for a in args {
            vals.push(self.arg(a, dot)?);
        }
        if let Some(p) = prev {
            vals.push(p);
        }
        let want = |n: usize| -> Result<(), String> {
            if vals.len() != n {
                Err(format!(
                    "wrong number of args for {f}: want {n} got {}",
                    vals.len()
                ))
            } else {
                Ok(())
            }
        };
        match f {
            "not" => {
                want(1)?;
                Ok(TV::Bool(!vals[0].truth()))
            }
            "eq" => {
                if vals.len() < 2 {
                    return Err("missing argument for comparison".into());
                }
                for b in &vals[1..] {
                    if equal(&vals[0], b).map_err(|e| format!("error calling eq: {e}"))? {
                        return Ok(TV::Bool(true));
                    }
                }
                Ok(TV::Bool(false))
            }
            "ne" => {
                want(2)?;
                Ok(TV::Bool(
                    !equal(&vals[0], &vals[1]).map_err(|e| format!("error calling ne: {e}"))?,
                ))
            }
            "lt" | "le" | "gt" | "ge" => {
                want(2)?;
                let (a, b) = (&vals[0], &vals[1]);
                let r = match f {
                    "lt" => less(a, b)?,
                    "le" => less(a, b)? || equal(a, b)?,
                    "gt" => !less(a, b)? && !equal(a, b)?,
                    _ => !less(a, b)?,
                };
                Ok(TV::Bool(r))
            }
            "index" => {
                if vals.is_empty() {
                    return Err("wrong number of args for index: want at least 1 got 0".into());
                }
                index(&vals[0], &vals[1..])
            }
            "len" => {
                want(1)?;
                Ok(TV::Int(match &vals[0] {
                    TV::Str(s) => s.len() as i64,
                    TV::List(l) => l.len() as i64,
                    TV::Map(m) => m.len() as i64,
                    v => return Err(format!("error calling len: len of type {}", v.kind())),
                }))
            }
            "print" => {
                let mut out = String::new();
                for (i, v) in vals.iter().enumerate() {
                    // Go's Sprint adds a space between operands when neither is a string
                    if i > 0 && !matches!(v, TV::Str(_)) && !matches!(vals[i - 1], TV::Str(_)) {
                        out.push(' ');
                    }
                    out.push_str(&v.print());
                }
                Ok(TV::Str(out))
            }
            "println" => Ok(TV::Str(
                vals.iter().map(TV::print).collect::<Vec<_>>().join(" ") + "\n",
            )),
            "printf" => {
                if vals.is_empty() {
                    return Err("wrong number of args for printf: want at least 1 got 0".into());
                }
                let TV::Str(format) = &vals[0] else {
                    return Err("printf: the format is not a string".into());
                };
                Ok(TV::Str(sprintf(format, &vals[1..])))
            }
            _ => match self.funcs.0.get(f) {
                Some(func) => func(&vals).map_err(|e| format!("error calling {f}: {e}")),
                None => Err(format!("function {f:?} not defined")),
            },
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn data() -> TV {
        TV::structure(
            "PromptData",
            vec![
                (
                    "Change",
                    TV::structure(
                        "Change",
                        vec![
                            ("ID", TV::str("C1")),
                            ("Title", TV::str("t")),
                            ("Intent", TV::str("do it")),
                        ],
                        json!({"id": "C1"}),
                    ),
                ),
                (
                    "Subject",
                    TV::from_json(&json!({"key": "R1", "props": {"a": 1}})),
                ),
                (
                    "Items",
                    TV::list(vec![
                        TV::from_json(&json!({"type": "x", "n": 1})),
                        TV::from_json(&json!({"type": "y", "n": 2.5})),
                    ]),
                ),
                ("Vars", TV::from_json(&json!({}))),
            ],
            json!({}),
        )
    }

    fn run(src: &str) -> String {
        let mut f = Funcs::default();
        f.add("json", |a| Ok(TV::Str(a[0].to_json().to_string())));
        Template::parse("t", src)
            .unwrap()
            .execute(&data(), &f)
            .unwrap()
    }

    #[test]
    fn renders_fields_ranges_and_conditions() {
        assert_eq!(
            run("{{ .Change.Intent }} / {{ .Subject.key }}"),
            "do it / R1"
        );
        assert_eq!(
            run("{{ range .Items }}[{{ .type }}={{ .n }}]{{ end }}"),
            "[x=1][y=2.5]"
        );
        assert_eq!(
            run("{{ if eq .Subject.key \"R1\" }}yes{{ else }}no{{ end }}"),
            "yes"
        );
        assert_eq!(
            run("{{ if eq .Subject.key \"R2\" }}a{{ else if .Change.ID }}b{{ end }}"),
            "b"
        );
        assert_eq!(run("{{ .Vars.missing }}"), "<no value>");
        assert_eq!(
            run("{{ with .Vars.event }}x{{ else }}none{{ end }}"),
            "none"
        );
        assert_eq!(run("{{ json .Subject.props }}"), "{\"a\":1}");
        assert_eq!(run("{{ printf \"impact:%s\" .Subject.key }}"), "impact:R1");
        assert_eq!(run("a  {{- \" b \" -}}  c"), "a b c");
        assert_eq!(
            run("{{ range $i, $x := .Items }}{{ $i }}{{ $.Change.ID }}{{ end }}"),
            "0C11C1"
        );
        assert_eq!(
            run("{{ if and (eq .Subject.key \"R1\") (index .Subject \"props\") }}ok{{ end }}"),
            "ok"
        );
        assert_eq!(run("{{/* comment */}}x"), "x");
        assert!(Template::parse("t", "{{ if .X }}").is_err());
        let err = Template::parse("t", "{{ .Change.Nope }}")
            .unwrap()
            .execute(&data(), &Funcs::default());
        assert!(err.is_err());
    }

    #[test]
    fn prints_like_go() {
        assert_eq!(go_float(0.7), "0.7");
        assert_eq!(go_float(3.0), "3");
        assert_eq!(go_float(1e21), "1e+21");
        assert_eq!(go_float(0.00001), "1e-05");
        assert_eq!(
            TV::from_json(&json!({"b": [1, "x"], "a": null})).print(),
            "map[a:<nil> b:[1 x]]"
        );
    }
}
