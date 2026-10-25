//! The expressions Casbin evaluates: Go's `github.com/casbin/govaluate` v1.3.0, read and run as it does. A rule is
//! lexed into tokens (`parseTokens`), checked against the transitions of its lexer states (`checkExpressionSyntax`),
//! planned into a tree of stages by precedence levels whose equal precedences are mirrored to run left to right
//! (`planStages`, `reorderStages`), then evaluated with Go's types: every number a float64, a string literal that
//! reads as a time its Unix seconds, `==` a deep equality, the type checks of each operator worded as Go words them.
//! The fold of literals at planning (`elideLiterals`) changes no result and is left out.
use std::sync::Arc;

use unicode_properties::{GeneralCategory, GeneralCategoryGroup, UnicodeGeneralCategory};

use crate::abac::{Resource, Subject};
use crate::goregexp::GoRegex;

/// A value of an expression, as Go holds it in an `interface{}`.
#[derive(Debug, Clone)]
pub enum Value {
    Nil,
    Bool(bool),
    Num(f64),
    Str(String),
    Pattern(Arc<GoRegex>),
    /// `[]interface{}`: the arguments of a call, the right side of `in`
    List(Vec<Value>),
    /// `[]string`: a field of a principal or a resource
    Strings(Vec<String>),
    Principal(Box<Subject>),
    Resource(Box<Resource>),
}

/// Why an evaluation stopped: an error the expression returns, or a panic of Go's runtime, which Casbin recovers
/// whole (`panic: …`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Fault {
    Error(String),
    Panic(String),
}

impl Fault {
    fn index(i: isize, len: usize) -> Fault {
        Fault::Panic(format!("runtime error: index out of range [{i}] with length {len}"))
    }

    fn nil() -> Fault {
        Fault::Panic("runtime error: invalid memory address or nil pointer dereference".into())
    }
}

fn err<T>(msg: impl Into<String>) -> Result<T, Fault> {
    Err(Fault::Error(msg.into()))
}

/// What an expression reaches outside itself: its functions and its parameters (`map[string]ExpressionFunction`,
/// `Parameters`).
pub trait Host {
    fn is_function(&self, name: &str) -> bool;
    fn call(&self, name: &str, args: Vec<Value>) -> Result<Value, Fault>;
    fn param(&self, name: &str) -> Result<Value, Fault>;
}

/// The value as Go's `%v` prints it.
pub fn sprint(v: &Value) -> String {
    match v {
        Value::Nil => "<nil>".into(),
        Value::Bool(b) => b.to_string(),
        Value::Num(f) => go_float(*f),
        Value::Str(s) => s.clone(),
        Value::Pattern(p) => p.source().to_string(),
        Value::List(l) => format!("[{}]", l.iter().map(sprint).collect::<Vec<_>>().join(" ")),
        Value::Strings(l) => format!("[{}]", l.join(" ")),
        Value::Principal(p) => format!(
            "{{{} {} {} [{}] [{}] {} {} {}}}",
            p.subject,
            p.org,
            p.project,
            p.roles.join(" "),
            p.global.join(" "),
            p.project_admin,
            p.project_owner,
            p.on_behalf_of
        ),
        Value::Resource(r) => format!(
            "{{{} {} {} {} {} {} {} {} [{}] {} [{}]}}",
            r.typ,
            r.id,
            r.org,
            r.namespace,
            r.owner,
            r.name,
            r.role,
            r.accountable,
            r.org_chain.join(" "),
            r.project_id,
            r.roles.join(" ")
        ),
    }
}

/// A float64 as `%v` prints it (`strconv.FormatFloat(f, 'g', -1, 64)`: an exponent below -4 or from 6 on).
pub fn go_float(f: f64) -> String {
    if f.is_nan() {
        return "NaN".into();
    }
    if f.is_infinite() {
        return if f > 0.0 { "+Inf".into() } else { "-Inf".into() };
    }
    if f == 0.0 {
        return if f.is_sign_negative() { "-0".into() } else { "0".into() };
    }
    let e = format!("{:e}", f.abs());
    let (mant, exp) = e.split_once('e').unwrap_or((&e, "0"));
    let exp: i32 = exp.parse().unwrap_or(0);
    let digits: Vec<char> = mant.chars().filter(char::is_ascii_digit).collect();
    let mut out = String::new();
    if f < 0.0 {
        out.push('-');
    }
    if !(-4..6).contains(&exp) {
        out.push(digits[0]);
        if digits.len() > 1 {
            out.push('.');
            out.extend(&digits[1..]);
        }
        out.push_str(&format!(
            "e{}{:02}",
            if exp < 0 { '-' } else { '+' },
            exp.unsigned_abs()
        ));
        return out;
    }
    let nd = digits.len() as i32;
    let dp = exp + 1;
    if dp <= 0 {
        out.push_str("0.");
        out.extend(std::iter::repeat_n('0', (-dp) as usize));
        out.extend(&digits);
    } else if dp >= nd {
        out.extend(&digits);
        out.extend(std::iter::repeat_n('0', (dp - nd) as usize));
    } else {
        out.extend(&digits[..dp as usize]);
        out.push('.');
        out.extend(&digits[dp as usize..]);
    }
    out
}

// ---------------------------------------------------------------------------------------------------------------
// Tokens

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Kind {
    Unknown,
    Prefix,
    Numeric,
    Boolean,
    Str,
    Pattern,
    Time,
    Variable,
    Function,
    Separator,
    Accessor,
    Comparator,
    LogicalOp,
    Modifier,
    Clause,
    ClauseClose,
    Ternary,
}

impl Kind {
    fn name(self) -> &'static str {
        match self {
            Kind::Unknown => "UNKNOWN",
            Kind::Prefix => "PREFIX",
            Kind::Numeric => "NUMERIC",
            Kind::Boolean => "BOOLEAN",
            Kind::Str => "STRING",
            Kind::Pattern => "PATTERN",
            Kind::Time => "TIME",
            Kind::Variable => "VARIABLE",
            Kind::Function => "FUNCTION",
            Kind::Separator => "SEPARATOR",
            Kind::Accessor => "ACCESSOR",
            Kind::Comparator => "COMPARATOR",
            Kind::LogicalOp => "LOGICALOP",
            Kind::Modifier => "MODIFIER",
            Kind::Clause => "CLAUSE",
            Kind::ClauseClose => "CLAUSE_CLOSE",
            Kind::Ternary => "TERNARY",
        }
    }

    /// The lexer state of a token kind (`validLexerStates`): whether an expression may end on it, and the kinds that
    /// may follow it.
    fn state(self) -> (bool, &'static [Kind]) {
        use Kind::*;
        match self {
            Unknown => (
                false,
                &[
                    Prefix, Numeric, Boolean, Variable, Pattern, Function, Accessor, Str, Time, Clause,
                ],
            ),
            Clause => (
                false,
                &[
                    Prefix,
                    Numeric,
                    Boolean,
                    Variable,
                    Pattern,
                    Function,
                    Accessor,
                    Str,
                    Time,
                    Clause,
                    ClauseClose,
                ],
            ),
            ClauseClose => (
                true,
                &[
                    Comparator,
                    Modifier,
                    Numeric,
                    Boolean,
                    Variable,
                    Str,
                    Pattern,
                    Time,
                    Clause,
                    ClauseClose,
                    LogicalOp,
                    Ternary,
                    Separator,
                ],
            ),
            Numeric | Boolean | Str => (
                true,
                &[Modifier, Comparator, LogicalOp, ClauseClose, Ternary, Separator],
            ),
            Time | Pattern => (true, &[Modifier, Comparator, LogicalOp, ClauseClose, Separator]),
            Variable => (
                true,
                &[Modifier, Comparator, LogicalOp, ClauseClose, Ternary, Separator],
            ),
            Modifier => (
                false,
                &[
                    Prefix,
                    Numeric,
                    Variable,
                    Function,
                    Accessor,
                    Str,
                    Boolean,
                    Clause,
                    ClauseClose,
                ],
            ),
            Comparator => (
                false,
                &[
                    Prefix,
                    Numeric,
                    Boolean,
                    Variable,
                    Function,
                    Accessor,
                    Str,
                    Time,
                    Clause,
                    ClauseClose,
                    Pattern,
                ],
            ),
            LogicalOp => (
                false,
                &[
                    Prefix,
                    Numeric,
                    Boolean,
                    Variable,
                    Function,
                    Accessor,
                    Str,
                    Time,
                    Clause,
                    ClauseClose,
                ],
            ),
            Prefix => (
                false,
                &[Numeric, Boolean, Variable, Function, Accessor, Clause, ClauseClose],
            ),
            Ternary => (
                false,
                &[
                    Prefix, Numeric, Boolean, Str, Time, Variable, Function, Accessor, Clause, Separator,
                ],
            ),
            Function => (false, &[Clause]),
            Accessor => (
                true,
                &[Clause, Modifier, Comparator, LogicalOp, ClauseClose, Ternary, Separator],
            ),
            Separator => (
                false,
                &[
                    Prefix, Numeric, Boolean, Str, Time, Variable, Function, Accessor, Clause,
                ],
            ),
        }
    }

    fn can_transition_to(self, k: Kind) -> bool {
        self.state().1.contains(&k)
    }
}

#[derive(Debug, Clone)]
enum TokVal {
    Nil,
    Text(String),
    Num(f64),
    Bool(bool),
    Time(crate::gotime::Time),
    Pattern(Arc<GoRegex>),
    Func(String),
    Path(Vec<String>),
    Rune(char),
}

impl TokVal {
    fn sprint(&self) -> String {
        match self {
            TokVal::Nil => "<nil>".into(),
            TokVal::Text(s) => s.clone(),
            TokVal::Num(f) => go_float(*f),
            TokVal::Bool(b) => b.to_string(),
            TokVal::Time(t) => t.to_string(),
            TokVal::Pattern(p) => p.source().to_string(),
            // a function prints as its address, which only Go's process knows
            TokVal::Func(_) => "0x0".into(),
            TokVal::Path(p) => format!("[{}]", p.join(" ")),
            TokVal::Rune(c) => (*c as u32).to_string(),
        }
    }

    fn text(&self) -> Option<&str> {
        match self {
            TokVal::Text(s) => Some(s),
            _ => None,
        }
    }
}

#[derive(Debug, Clone)]
struct Token {
    kind: Kind,
    val: TokVal,
}

const NO_TOKEN: Token = Token {
    kind: Kind::Unknown,
    val: TokVal::Nil,
};

// ---------------------------------------------------------------------------------------------------------------
// The lexer (`parsing.go`)

struct Stream {
    src: Vec<char>,
    pos: isize,
}

impl Stream {
    fn can_read(&self) -> bool {
        self.pos < self.src.len() as isize
    }

    fn read(&mut self) -> Result<char, Fault> {
        let c = usize::try_from(self.pos)
            .ok()
            .and_then(|i| self.src.get(i).copied())
            .ok_or_else(|| Fault::index(self.pos, self.src.len()))?;
        self.pos += 1;
        Ok(c)
    }

    fn rewind(&mut self, n: isize) {
        self.pos -= n;
    }
}

fn is_space(c: char) -> bool {
    c.is_whitespace()
}

fn is_letter(c: char) -> bool {
    c.general_category_group() == GeneralCategoryGroup::Letter
}

fn is_digit(c: char) -> bool {
    c.general_category() == GeneralCategory::DecimalNumber
}

fn is_numeric(c: char) -> bool {
    is_digit(c) || c == '.'
}

fn is_hex(c: char) -> bool {
    is_digit(c) || matches!(c.to_ascii_lowercase(), 'a'..='f')
}

fn is_not_quote(c: char) -> bool {
    c != '\'' && c != '"'
}

fn is_not_alphanumeric(c: char) -> bool {
    !(is_digit(c) || is_letter(c) || matches!(c, '(' | ')' | '[' | ']') || !is_not_quote(c))
}

fn is_variable_name(c: char) -> bool {
    is_letter(c) || is_digit(c) || c == '_' || c == '.'
}

/// `readUntilFalse`: what reads until the condition fails or a whitespace breaks it; false when the stream ended first.
fn read_until_false(
    s: &mut Stream,
    include_whitespace: bool,
    break_whitespace: bool,
    allow_escaping: bool,
    cond: fn(char) -> bool,
) -> Result<(String, bool), Fault> {
    let mut buf = String::new();
    let mut conditioned = false;
    while s.can_read() {
        let mut c = s.read()?;
        if allow_escaping && c == '\\' {
            c = s.read()?;
            buf.push(c);
            continue;
        }
        if is_space(c) {
            if break_whitespace && !buf.is_empty() {
                conditioned = true;
                break;
            }
            if !include_whitespace {
                continue;
            }
        }
        if cond(c) {
            buf.push(c);
        } else {
            conditioned = true;
            s.rewind(1);
            break;
        }
    }
    Ok((buf, conditioned))
}

fn read_token_until_false(s: &mut Stream, cond: fn(char) -> bool) -> Result<String, Fault> {
    s.rewind(1);
    Ok(read_until_false(s, false, true, true, cond)?.0)
}

fn prefix_symbol(s: &str) -> Option<Sym> {
    Some(match s {
        "-" => Sym::Negate,
        "!" => Sym::Invert,
        "~" => Sym::BitwiseNot,
        _ => return None,
    })
}

fn modifier_symbol(s: &str) -> Option<Sym> {
    additive_symbol(s)
        .or_else(|| multiplicative_symbol(s))
        .or_else(|| exponential_symbol(s))
        .or_else(|| bitwise_symbol(s))
        .or_else(|| shift_symbol(s))
}

fn logical_symbol(s: &str) -> Option<Sym> {
    Some(match s {
        "&&" => Sym::And,
        "||" => Sym::Or,
        _ => return None,
    })
}

fn comparator_symbol(s: &str) -> Option<Sym> {
    Some(match s {
        "==" => Sym::Eq,
        "!=" => Sym::Neq,
        ">" => Sym::Gt,
        ">=" => Sym::Gte,
        "<" => Sym::Lt,
        "<=" => Sym::Lte,
        "=~" => Sym::Req,
        "!~" => Sym::Nreq,
        "in" => Sym::In,
        _ => return None,
    })
}

fn ternary_symbol(s: &str) -> Option<Sym> {
    Some(match s {
        "?" => Sym::TernaryTrue,
        ":" => Sym::TernaryFalse,
        "??" => Sym::Coalesce,
        _ => return None,
    })
}

fn bitwise_symbol(s: &str) -> Option<Sym> {
    Some(match s {
        "^" => Sym::BitwiseXor,
        "&" => Sym::BitwiseAnd,
        "|" => Sym::BitwiseOr,
        _ => return None,
    })
}

fn shift_symbol(s: &str) -> Option<Sym> {
    Some(match s {
        ">>" => Sym::BitwiseRshift,
        "<<" => Sym::BitwiseLshift,
        _ => return None,
    })
}

fn additive_symbol(s: &str) -> Option<Sym> {
    Some(match s {
        "+" => Sym::Plus,
        "-" => Sym::Minus,
        _ => return None,
    })
}

fn multiplicative_symbol(s: &str) -> Option<Sym> {
    Some(match s {
        "*" => Sym::Multiply,
        "/" => Sym::Divide,
        "%" => Sym::Modulus,
        _ => return None,
    })
}

fn exponential_symbol(s: &str) -> Option<Sym> {
    (s == "**").then_some(Sym::Exponent)
}

fn separator_symbol(s: &str) -> Option<Sym> {
    (s == ",").then_some(Sym::Separate)
}

/// `readToken`: the next token after the state of the last one, none when only whitespace is left.
fn read_token(s: &mut Stream, state: Kind, host: &dyn Host) -> Result<Option<Token>, Fault> {
    let mut kind = Kind::Unknown;
    let mut val = TokVal::Nil;
    while s.can_read() {
        let mut c = s.read()?;
        if is_space(c) {
            continue;
        }
        if is_numeric(c) {
            if s.can_read() && c == '0' {
                c = s.read()?;
                if s.can_read() && c == 'x' {
                    let (t, _) = read_until_false(s, false, true, true, is_hex)?;
                    let v = (!t.is_empty() && t.chars().all(|c| c.is_ascii_hexdigit()))
                        .then(|| u64::from_str_radix(&t, 16).ok())
                        .flatten()
                        .ok_or_else(|| Fault::Error(format!("Unable to parse hex value '{t}' to uint64\n")))?;
                    kind = Kind::Numeric;
                    val = TokVal::Num(v as f64);
                    break;
                }
                s.rewind(1);
            }
            let t = read_token_until_false(s, is_numeric)?;
            let v = parse_float(&t)
                .ok_or_else(|| Fault::Error(format!("Unable to parse numeric value '{t}' to float64\n")))?;
            kind = Kind::Numeric;
            val = TokVal::Num(v);
            break;
        }
        if c == ',' {
            kind = Kind::Separator;
            val = TokVal::Text(",".into());
            break;
        }
        if c == '[' {
            let (t, completed) = read_until_false(s, true, false, true, |c| c != ']')?;
            if !completed {
                return err("Unclosed parameter bracket");
            }
            s.rewind(-1);
            kind = Kind::Variable;
            val = TokVal::Text(t);
            break;
        }
        if is_letter(c) {
            let t = read_token_until_false(s, is_variable_name)?;
            kind = Kind::Variable;
            val = TokVal::Text(t.clone());
            if t == "true" {
                kind = Kind::Boolean;
                val = TokVal::Bool(true);
            } else if t == "false" {
                kind = Kind::Boolean;
                val = TokVal::Bool(false);
            }
            if t == "in" || t == "IN" {
                kind = Kind::Comparator;
                val = TokVal::Text("in".into());
            }
            if host.is_function(&t) {
                kind = Kind::Function;
                val = TokVal::Func(t.clone());
            }
            if t.find('.').is_some_and(|i| i > 0) {
                if t.ends_with('.') {
                    return err(format!("Hanging accessor on token '{t}'"));
                }
                kind = Kind::Accessor;
                val = TokVal::Path(t.split('.').map(str::to_string).collect());
            }
            break;
        }
        if !is_not_quote(c) {
            let (t, completed) = read_until_false(s, true, false, true, is_not_quote)?;
            if !completed {
                return err("Unclosed string literal");
            }
            s.rewind(-1);
            match crate::gotime::try_parse(&t) {
                Some(time) => {
                    kind = Kind::Time;
                    val = TokVal::Time(time);
                }
                None => {
                    kind = Kind::Str;
                    val = TokVal::Text(t);
                }
            }
            break;
        }
        if c == '(' {
            kind = Kind::Clause;
            val = TokVal::Rune(c);
            break;
        }
        if c == ')' {
            kind = Kind::ClauseClose;
            val = TokVal::Rune(c);
            break;
        }
        let t = read_token_until_false(s, is_not_alphanumeric)?;
        val = TokVal::Text(t.clone());
        if state.can_transition_to(Kind::Prefix) && prefix_symbol(&t).is_some() {
            kind = Kind::Prefix;
            break;
        }
        kind = if modifier_symbol(&t).is_some() {
            Kind::Modifier
        } else if logical_symbol(&t).is_some() {
            Kind::LogicalOp
        } else if comparator_symbol(&t).is_some() {
            Kind::Comparator
        } else if ternary_symbol(&t).is_some() {
            Kind::Ternary
        } else {
            return err(format!("Invalid token: '{t}'"));
        };
        break;
    }
    Ok((kind != Kind::Unknown).then_some(Token { kind, val }))
}

/// Go's `strconv.ParseFloat(s, 64)` of what a number token reads (digits, dots and what a backslash escapes): the
/// decimal form, infinities and NaN; an overflow is refused.
fn parse_float(s: &str) -> Option<f64> {
    let body = s.strip_prefix(['+', '-']).unwrap_or(s);
    let lower = body.to_ascii_lowercase();
    if lower == "inf" || lower == "infinity" {
        return Some(if s.starts_with('-') {
            f64::NEG_INFINITY
        } else {
            f64::INFINITY
        });
    }
    if s.eq_ignore_ascii_case("nan") {
        return Some(f64::NAN);
    }
    let b = body.as_bytes();
    let mut i = 0;
    let mut digits = 0;
    while i < b.len() && b[i].is_ascii_digit() {
        i += 1;
        digits += 1;
    }
    if i < b.len() && b[i] == b'.' {
        i += 1;
        while i < b.len() && b[i].is_ascii_digit() {
            i += 1;
            digits += 1;
        }
    }
    if digits == 0 {
        return None;
    }
    if i < b.len() && (b[i] == b'e' || b[i] == b'E') {
        i += 1;
        if i < b.len() && (b[i] == b'+' || b[i] == b'-') {
            i += 1;
        }
        let start = i;
        while i < b.len() && b[i].is_ascii_digit() {
            i += 1;
        }
        if i == start {
            return None;
        }
    }
    if i != b.len() {
        return None;
    }
    let v: f64 = s.parse().ok()?;
    v.is_finite().then_some(v)
}

fn parse_tokens(expr: &str, host: &dyn Host) -> Result<Vec<Token>, Fault> {
    let mut s = Stream {
        src: expr.chars().collect(),
        pos: 0,
    };
    let mut state = Kind::Unknown;
    let mut out = Vec::new();
    while s.can_read() {
        let Some(t) = read_token(&mut s, state, host)? else {
            break;
        };
        state = t.kind;
        out.push(t);
    }
    check_balance(&out)?;
    Ok(out)
}

fn check_balance(tokens: &[Token]) -> Result<(), Fault> {
    let mut parens = 0;
    for t in tokens {
        match t.kind {
            Kind::Clause => parens += 1,
            Kind::ClauseClose => parens -= 1,
            _ => {}
        }
    }
    if parens != 0 {
        return err("Unbalanced parenthesis");
    }
    Ok(())
}

fn check_syntax(tokens: &[Token]) -> Result<(), Fault> {
    let mut state = Kind::Unknown;
    let mut last = &NO_TOKEN;
    for t in tokens {
        if !state.can_transition_to(t.kind) {
            if last.kind == Kind::Variable && t.kind == Kind::Clause {
                return err(format!("Undefined function {}", last.val.sprint()));
            }
            return err(format!(
                "Cannot transition token types from {} [{}] to {} [{}]",
                state.name(),
                last.val.sprint(),
                t.kind.name(),
                t.val.sprint()
            ));
        }
        state = t.kind;
        last = t;
    }
    if !state.state().0 {
        return err("Unexpected end of expression");
    }
    Ok(())
}

/// `optimizeTokens`: the string right of a regular expression operator compiled once.
fn optimize_tokens(tokens: &mut [Token]) -> Result<(), Fault> {
    for i in 0..tokens.len() {
        if tokens[i].kind != Kind::Comparator {
            continue;
        }
        if !matches!(tokens[i].val.text(), Some("=~" | "!~")) {
            continue;
        }
        let Some(next) = tokens.get_mut(i + 1) else {
            return Err(Fault::index(i as isize + 1, tokens.len()));
        };
        if next.kind == Kind::Str {
            let TokVal::Text(src) = &next.val else { continue };
            let re = GoRegex::compile(src).map_err(Fault::Error)?;
            next.kind = Kind::Pattern;
            next.val = TokVal::Pattern(Arc::new(re));
        }
    }
    Ok(())
}

// ---------------------------------------------------------------------------------------------------------------
// The planner (`stagePlanner.go`)

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Sym {
    Value,
    Literal,
    Noop,
    Eq,
    Neq,
    Gt,
    Lt,
    Gte,
    Lte,
    Req,
    Nreq,
    In,
    And,
    Or,
    Plus,
    Minus,
    BitwiseAnd,
    BitwiseOr,
    BitwiseXor,
    BitwiseLshift,
    BitwiseRshift,
    Multiply,
    Divide,
    Modulus,
    Exponent,
    Negate,
    Invert,
    BitwiseNot,
    TernaryTrue,
    TernaryFalse,
    Coalesce,
    Functional,
    Access,
    Separate,
}

impl Sym {
    fn name(self) -> &'static str {
        use Sym::*;
        match self {
            Noop => "NOOP",
            Value => "VALUE",
            Eq => "=",
            Neq => "!=",
            Gt => ">",
            Lt => "<",
            Gte => ">=",
            Lte => "<=",
            Req => "=~",
            Nreq => "!~",
            And => "&&",
            Or => "||",
            In => "in",
            BitwiseAnd => "&",
            BitwiseOr => "|",
            BitwiseXor => "^",
            BitwiseLshift => "<<",
            BitwiseRshift => ">>",
            Plus => "+",
            Minus => "-",
            Multiply => "*",
            Divide => "/",
            Modulus => "%",
            Exponent => "**",
            Negate => "-",
            Invert => "!",
            BitwiseNot => "~",
            TernaryTrue => "?",
            TernaryFalse => ":",
            Coalesce => "??",
            Literal | Functional | Access | Separate => "",
        }
    }

    /// `findOperatorPrecedenceForSymbol`
    fn precedence(self) -> u8 {
        use Sym::*;
        match self {
            Noop => 0,
            Value => 1,
            Functional | Access => 2,
            Negate | Invert | BitwiseNot => 3,
            Exponent => 4,
            Plus | Minus => 5,
            BitwiseAnd | BitwiseOr | BitwiseXor => 6,
            BitwiseLshift | BitwiseRshift => 7,
            Multiply | Divide | Modulus => 8,
            Eq | Neq | Gt | Lt | Gte | Lte | Req | Nreq | In => 9,
            TernaryTrue | TernaryFalse | Coalesce => 10,
            And => 11,
            Or => 12,
            Separate => 13,
            Literal => 1,
        }
    }

    fn short_circuits(self) -> bool {
        matches!(
            self,
            Sym::And | Sym::Or | Sym::TernaryTrue | Sym::TernaryFalse | Sym::Coalesce
        )
    }
}

#[derive(Debug, Clone, Copy)]
enum Check {
    String,
    RegexOrString,
    Bool,
    Float,
    Array,
}

#[derive(Debug, Clone, Copy)]
enum Combined {
    Comparator,
    Addition,
}

#[derive(Debug, Clone, Copy, Default)]
struct Checks {
    left: Option<Check>,
    right: Option<Check>,
    combined: Option<Combined>,
}

/// `findTypeChecks`
fn type_checks(sym: Sym) -> Checks {
    use Sym::*;
    let lr = |l: Option<Check>, r: Option<Check>| Checks {
        left: l,
        right: r,
        combined: None,
    };
    match sym {
        Gt | Lt | Gte | Lte => Checks {
            combined: Some(Combined::Comparator),
            ..Checks::default()
        },
        Req | Nreq => lr(Some(Check::String), Some(Check::RegexOrString)),
        And | Or => lr(Some(Check::Bool), Some(Check::Bool)),
        In => lr(None, Some(Check::Array)),
        BitwiseLshift | BitwiseRshift | BitwiseOr | BitwiseAnd | BitwiseXor => {
            lr(Some(Check::Float), Some(Check::Float))
        }
        Plus => Checks {
            combined: Some(Combined::Addition),
            ..Checks::default()
        },
        Minus | Multiply | Divide | Modulus | Exponent => lr(Some(Check::Float), Some(Check::Float)),
        Negate | BitwiseNot => lr(None, Some(Check::Float)),
        Invert => lr(None, Some(Check::Bool)),
        TernaryTrue => lr(Some(Check::Bool), None),
        _ => Checks::default(),
    }
}

const LOGICAL_ERROR: &str = "Value '%v' cannot be used with the logical operator '%v', it is not a bool";
const MODIFIER_ERROR: &str = "Value '%v' cannot be used with the modifier '%v', it is not a number";
const COMPARATOR_ERROR: &str = "Value '%v' cannot be used with the comparator '%v', it is not a number";
const TERNARY_ERROR: &str = "Value '%v' cannot be used with the ternary operator '%v', it is not a bool";
const PREFIX_ERROR: &str = "Value '%v' cannot be used with the prefix '%v'";

#[derive(Debug, Clone)]
enum Op {
    /// the operator of the symbol (`stageSymbolMap`)
    Symbol,
    Noop,
    Literal(Value),
    Param(String),
    Function(String),
    Accessor(Vec<String>),
    EnsureSlice(Box<Op>),
}

/// What a stage does, which a mirror swaps between stages (`setToNonStage`).
#[derive(Debug, Clone)]
struct Body {
    sym: Sym,
    op: Op,
    checks: Checks,
    format: &'static str,
}

#[derive(Debug, Clone)]
struct Stage {
    body: Body,
    left: Option<usize>,
    right: Option<usize>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Level {
    Separator,
    Ternary,
    LogicalOr,
    LogicalAnd,
    Comparator,
    Bitwise,
    Shift,
    Additive,
    Multiplicative,
    Exponential,
    Prefix,
}

#[derive(Clone, Copy)]
enum Precedent {
    Level(Level),
    Function,
}

struct Spec {
    symbols: fn(&str) -> Option<Sym>,
    kinds: &'static [Kind],
    format: &'static str,
    next: Option<Precedent>,
    next_right: Option<Precedent>,
}

fn spec(l: Level) -> Spec {
    use Level::*;
    let level = |symbols, kinds, format, next| Spec {
        symbols,
        kinds,
        format,
        next: Some(next),
        next_right: None,
    };
    match l {
        Prefix => Spec {
            symbols: prefix_symbol,
            kinds: &[Kind::Prefix],
            format: PREFIX_ERROR,
            next: None,
            next_right: Some(Precedent::Function),
        },
        Exponential => level(
            exponential_symbol,
            &[Kind::Modifier],
            MODIFIER_ERROR,
            Precedent::Function,
        ),
        Multiplicative => level(
            multiplicative_symbol,
            &[Kind::Modifier],
            MODIFIER_ERROR,
            Precedent::Level(Exponential),
        ),
        Additive => level(
            additive_symbol,
            &[Kind::Modifier],
            MODIFIER_ERROR,
            Precedent::Level(Multiplicative),
        ),
        Shift => level(
            shift_symbol,
            &[Kind::Modifier],
            MODIFIER_ERROR,
            Precedent::Level(Additive),
        ),
        Bitwise => level(
            bitwise_symbol,
            &[Kind::Modifier],
            MODIFIER_ERROR,
            Precedent::Level(Shift),
        ),
        Comparator => level(
            comparator_symbol,
            &[Kind::Comparator],
            COMPARATOR_ERROR,
            Precedent::Level(Bitwise),
        ),
        LogicalAnd => level(
            |s| (s == "&&").then_some(Sym::And),
            &[Kind::LogicalOp],
            LOGICAL_ERROR,
            Precedent::Level(Comparator),
        ),
        LogicalOr => level(
            |s| (s == "||").then_some(Sym::Or),
            &[Kind::LogicalOp],
            LOGICAL_ERROR,
            Precedent::Level(LogicalAnd),
        ),
        Ternary => level(
            ternary_symbol,
            &[Kind::Ternary],
            TERNARY_ERROR,
            Precedent::Level(LogicalOr),
        ),
        Separator => level(separator_symbol, &[Kind::Separator], "", Precedent::Level(Ternary)),
    }
}

struct TokenStream {
    tokens: Vec<Token>,
    index: isize,
}

impl TokenStream {
    fn has_next(&self) -> bool {
        self.index < self.tokens.len() as isize
    }

    fn next(&mut self) -> Result<Token, Fault> {
        let t = usize::try_from(self.index)
            .ok()
            .and_then(|i| self.tokens.get(i).cloned())
            .ok_or_else(|| Fault::index(self.index, self.tokens.len()))?;
        self.index += 1;
        Ok(t)
    }

    fn rewind(&mut self) {
        self.index -= 1;
    }
}

struct Planner {
    stream: TokenStream,
    stages: Vec<Stage>,
}

impl Planner {
    fn push(&mut self, body: Body, left: Option<usize>, right: Option<usize>) -> usize {
        self.stages.push(Stage { body, left, right });
        self.stages.len() - 1
    }

    fn plan(&mut self, p: Precedent) -> Result<Option<usize>, Fault> {
        match p {
            Precedent::Level(l) => self.level(l),
            Precedent::Function => self.function(),
        }
    }

    fn tokens(&mut self) -> Result<Option<usize>, Fault> {
        if !self.stream.has_next() {
            return Ok(None);
        }
        self.level(Level::Separator)
    }

    /// `planPrecedenceLevel`: the left precedent, then an operator of this level and its right side; the stream is
    /// rewound by one when no operator follows, also past its end, as Go does.
    fn level(&mut self, l: Level) -> Result<Option<usize>, Fault> {
        let spec = spec(l);
        let left = match spec.next {
            Some(p) => self.plan(p)?,
            None => None,
        };
        if self.stream.has_next() {
            let t = self.stream.next()?;
            let sym = spec
                .kinds
                .contains(&t.kind)
                .then(|| t.val.text().and_then(spec.symbols))
                .flatten();
            let Some(sym) = sym else {
                self.stream.rewind();
                return Ok(left);
            };
            let right = self.plan(spec.next_right.unwrap_or(Precedent::Level(l)))?;
            let body = Body {
                sym,
                op: Op::Symbol,
                checks: type_checks(sym),
                format: spec.format,
            };
            return Ok(Some(self.push(body, left, right)));
        }
        self.stream.rewind();
        Ok(left)
    }

    fn function(&mut self) -> Result<Option<usize>, Fault> {
        let t = self.stream.next()?;
        let TokVal::Func(name) = t.val else {
            self.stream.rewind();
            return self.accessor();
        };
        let right = self.accessor()?;
        let body = Body {
            sym: Sym::Functional,
            op: Op::Function(name),
            checks: Checks::default(),
            format: "Unable to run function '%v': %v",
        };
        Ok(Some(self.push(body, None, right)))
    }

    fn accessor(&mut self) -> Result<Option<usize>, Fault> {
        if !self.stream.has_next() {
            return Ok(None);
        }
        let t = self.stream.next()?;
        let TokVal::Path(path) = t.val else {
            self.stream.rewind();
            return self.value();
        };
        let mut right = None;
        if self.stream.has_next() {
            let other = self.stream.next()?;
            self.stream.rewind();
            if other.kind == Kind::Clause {
                right = self.tokens()?;
            }
        }
        let body = Body {
            sym: Sym::Access,
            op: Op::Accessor(path),
            checks: Checks::default(),
            format: "Unable to access parameter field or method '%v': %v",
        };
        Ok(Some(self.push(body, None, right)))
    }

    fn value(&mut self) -> Result<Option<usize>, Fault> {
        if !self.stream.has_next() {
            return Ok(None);
        }
        let t = self.stream.next()?;
        let (sym, op) = match t.kind {
            Kind::Clause => {
                let prev = match self.stream.index {
                    i if i > 1 => self.stream.tokens[i as usize - 2].clone(),
                    _ => NO_TOKEN,
                };
                let ret = self.tokens()?;
                if prev.kind == Kind::Comparator && prev.val.text() == Some("in") {
                    let i = ret.ok_or_else(Fault::nil)?;
                    if self.stages[i].body.sym == Sym::Literal {
                        let op = std::mem::replace(&mut self.stages[i].body.op, Op::Noop);
                        self.stages[i].body.op = Op::EnsureSlice(Box::new(op));
                    }
                }
                self.stream.next()?;
                let body = Body {
                    sym: Sym::Noop,
                    op: Op::Noop,
                    checks: Checks::default(),
                    format: "",
                };
                return Ok(Some(self.push(body, None, ret)));
            }
            Kind::ClauseClose => {
                self.stream.rewind();
                return Ok(None);
            }
            Kind::Prefix => {
                self.stream.rewind();
                return self.level(Level::Prefix);
            }
            Kind::Variable => match &t.val {
                TokVal::Text(n) => (Sym::Value, Op::Param(n.clone())),
                _ => return Err(Fault::nil()),
            },
            Kind::Numeric | Kind::Str | Kind::Pattern | Kind::Boolean => {
                let v = match &t.val {
                    TokVal::Num(f) => Value::Num(*f),
                    TokVal::Text(s) => Value::Str(s.clone()),
                    TokVal::Pattern(p) => Value::Pattern(p.clone()),
                    TokVal::Bool(b) => Value::Bool(*b),
                    _ => Value::Nil,
                };
                (Sym::Literal, Op::Literal(v))
            }
            Kind::Time => match &t.val {
                TokVal::Time(time) => (Sym::Literal, Op::Literal(Value::Num(time.unix() as f64))),
                _ => return Err(Fault::nil()),
            },
            _ => {
                return err(format!(
                    "Unable to plan token kind: '{}', value: '{}'",
                    t.kind.name(),
                    t.val.sprint()
                ));
            }
        };
        let body = Body {
            sym,
            op,
            checks: Checks::default(),
            format: "",
        };
        Ok(Some(self.push(body, None, None)))
    }

    /// `reorderStages`: the runs of stages of equal precedence down the right side mirrored, so that they evaluate
    /// left to right.
    fn reorder(&mut self, root: usize) {
        let mut identical: Vec<usize> = Vec::new();
        let mut next = Some(root);
        let mut precedence = self.stages[root].body.sym.precedence();
        while let Some(cur) = next {
            next = self.stages[cur].right;
            if let Some(l) = self.stages[cur].left {
                self.reorder(l);
            }
            let p = self.stages[cur].body.sym.precedence();
            if p == precedence {
                identical.push(cur);
                continue;
            }
            if identical.len() > 1 {
                self.mirror(&identical);
            }
            identical = vec![cur];
            precedence = p;
        }
        if identical.len() > 1 {
            self.mirror(&identical);
        }
    }

    /// `mirrorStageSubtree`
    fn mirror(&mut self, stages: &[usize]) {
        let n = stages.len();
        for &f in stages {
            let s = &mut self.stages[f];
            std::mem::swap(&mut s.left, &mut s.right);
        }
        let (root, front) = (stages[0], stages[n - 1]);
        let carry = self.stages[front].left;
        self.stages[front].left = self.stages[root].right;
        self.stages[root].right = carry;
        for i in 0..(n - 2) / 2 + 1 {
            let (front, inverse) = (stages[i + 1], stages[n - i - 1]);
            let carry = self.stages[front].right;
            self.stages[front].right = self.stages[inverse].right;
            self.stages[inverse].right = carry;
        }
        for i in 0..n / 2 {
            let (a, b) = (stages[i], stages[n - i - 1]);
            let body = self.stages[a].body.clone();
            self.stages[a].body = std::mem::replace(&mut self.stages[b].body, body);
        }
    }
}

/// An expression read and planned (`EvaluableExpression`).
#[derive(Debug, Clone)]
pub struct Expression {
    stages: Vec<Stage>,
    root: Option<usize>,
}

impl Expression {
    /// `NewEvaluableExpressionWithFunctions`
    pub fn new(expr: &str, host: &dyn Host) -> Result<Expression, Fault> {
        let mut tokens = parse_tokens(expr, host)?;
        check_balance(&tokens)?;
        check_syntax(&tokens)?;
        optimize_tokens(&mut tokens)?;
        let mut p = Planner {
            stream: TokenStream { tokens, index: 0 },
            stages: Vec::new(),
        };
        let root = p.tokens()?.ok_or_else(Fault::nil)?;
        p.reorder(root);
        Ok(Expression {
            stages: p.stages,
            root: Some(root),
        })
    }

    /// `Eval`
    pub fn eval(&self, host: &dyn Host) -> Result<Value, Fault> {
        match self.root {
            Some(r) => self.stage(r, host),
            None => Ok(Value::Nil),
        }
    }

    fn stage(&self, i: usize, host: &dyn Host) -> Result<Value, Fault> {
        let st = &self.stages[i];
        let sym = st.body.sym;
        let left = match st.left {
            Some(l) => self.stage(l, host)?,
            None => Value::Nil,
        };
        let mut skip_right = false;
        if sym.short_circuits() {
            match sym {
                Sym::And if matches!(left, Value::Bool(false)) => return Ok(Value::Bool(false)),
                Sym::Or if matches!(left, Value::Bool(true)) => return Ok(Value::Bool(true)),
                Sym::Coalesce if !matches!(left, Value::Nil) => return Ok(left),
                Sym::TernaryTrue if matches!(left, Value::Bool(false)) => skip_right = true,
                Sym::TernaryFalse if !matches!(left, Value::Nil) => skip_right = true,
                _ => {}
            }
        }
        let right = match st.right {
            Some(r) if !skip_right => self.stage(r, host)?,
            _ => Value::Nil,
        };
        let c = st.body.checks;
        match c.combined {
            None => {
                for (check, v) in [(c.left, &left), (c.right, &right)] {
                    if let Some(check) = check
                        && !passes(check, v)
                    {
                        return err(type_error(st.body.format, v, sym));
                    }
                }
            }
            Some(comb) => {
                if !combined(comb, &left, &right) {
                    return err(type_error(st.body.format, &left, sym));
                }
            }
        }
        operate(&st.body.op, sym, left, right, host)
    }
}

fn type_error(format: &str, v: &Value, sym: Sym) -> String {
    format.replacen("%v", &sprint(v), 1).replacen("%v", sym.name(), 1)
}

fn passes(c: Check, v: &Value) -> bool {
    match c {
        Check::String => matches!(v, Value::Str(_)),
        Check::RegexOrString => matches!(v, Value::Str(_) | Value::Pattern(_)),
        Check::Bool => matches!(v, Value::Bool(_)),
        Check::Float => matches!(v, Value::Num(_)),
        Check::Array => matches!(v, Value::List(_)),
    }
}

fn combined(c: Combined, l: &Value, r: &Value) -> bool {
    matches!(
        (c, l, r),
        (_, Value::Num(_), Value::Num(_))
            | (Combined::Comparator, Value::Str(_), Value::Str(_))
            | (Combined::Addition, Value::Str(_), _)
            | (Combined::Addition, _, Value::Str(_))
    )
}

/// `reflect.DeepEqual` of two values.
fn deep_equal(l: &Value, r: &Value) -> bool {
    match (l, r) {
        (Value::Nil, Value::Nil) => true,
        (Value::Bool(a), Value::Bool(b)) => a == b,
        (Value::Num(a), Value::Num(b)) => a == b,
        (Value::Str(a), Value::Str(b)) => a == b,
        (Value::Pattern(a), Value::Pattern(b)) => Arc::ptr_eq(a, b) || a.source() == b.source(),
        (Value::List(a), Value::List(b)) => a.len() == b.len() && a.iter().zip(b).all(|(x, y)| deep_equal(x, y)),
        (Value::Strings(a), Value::Strings(b)) => a == b,
        (Value::Principal(a), Value::Principal(b)) => a == b,
        (Value::Resource(a), Value::Resource(b)) => a == b,
        _ => false,
    }
}

/// `left == value` of two interfaces: false between types, a panic for two values of a type Go cannot compare.
fn interface_equal(l: &Value, r: &Value) -> Result<bool, Fault> {
    let uncomparable = |t: &str| Fault::Panic(format!("runtime error: comparing uncomparable type {t}"));
    match (l, r) {
        (Value::List(_), Value::List(_)) => Err(uncomparable("[]interface {}")),
        (Value::Strings(_), Value::Strings(_)) => Err(uncomparable("[]string")),
        (Value::Principal(_), Value::Principal(_)) => Err(uncomparable("authz.Principal")),
        (Value::Resource(_), Value::Resource(_)) => Err(uncomparable("authz.Resource")),
        (Value::Pattern(a), Value::Pattern(b)) => Ok(Arc::ptr_eq(a, b)),
        _ => Ok(deep_equal(l, r)),
    }
}

/// 2^63, the first float64 past the int64s.
const TWO63: f64 = 9_223_372_036_854_775_808.0;

/// A float64 converted to int64 as amd64 does: NaN and what lies out of range give the smallest int64.
fn to_i64(f: f64) -> i64 {
    if !(-TWO63..TWO63).contains(&f) {
        i64::MIN
    } else {
        f as i64
    }
}

/// A float64 converted to uint64 as Go's compiler does on amd64.
fn to_u64(f: f64) -> u64 {
    if f < TWO63 {
        to_i64(f) as u64
    } else {
        (to_i64(f - TWO63) as u64) ^ (1 << 63)
    }
}

fn num(v: &Value) -> f64 {
    match v {
        Value::Num(f) => *f,
        _ => 0.0,
    }
}

fn operate(op: &Op, sym: Sym, left: Value, right: Value, host: &dyn Host) -> Result<Value, Fault> {
    match op {
        Op::Noop => Ok(right),
        Op::Literal(v) => Ok(v.clone()),
        Op::Param(n) => host.param(n),
        Op::EnsureSlice(inner) => Ok(Value::List(vec![operate(inner, sym, left, right, host)?])),
        Op::Function(name) => {
            let args = match right {
                Value::Nil => Vec::new(),
                Value::List(l) => l,
                v => vec![v],
            };
            host.call(name, args)
        }
        Op::Accessor(path) => access(path, right, host),
        Op::Symbol => symbol(sym, left, right),
    }
}

fn symbol(sym: Sym, left: Value, right: Value) -> Result<Value, Fault> {
    use Sym::{
        Access, And, BitwiseAnd, BitwiseLshift, BitwiseNot, BitwiseOr, BitwiseRshift, BitwiseXor, Coalesce, Divide, Eq,
        Exponent, Functional, Gt, Gte, In, Invert, Literal, Lt, Lte, Minus, Modulus, Multiply, Negate, Neq, Noop, Nreq,
        Or, Plus, Req, Separate, TernaryFalse, TernaryTrue,
    };
    let (l, r) = (num(&left), num(&right));
    let cmp = |f: fn(&str, &str) -> bool, g: fn(f64, f64) -> bool| match (&left, &right) {
        (Value::Str(a), Value::Str(b)) => Value::Bool(f(a, b)),
        _ => Value::Bool(g(l, r)),
    };
    Ok(match sym {
        Eq => Value::Bool(deep_equal(&left, &right)),
        Neq => Value::Bool(!deep_equal(&left, &right)),
        Gt => cmp(|a, b| a > b, |a, b| a > b),
        Lt => cmp(|a, b| a < b, |a, b| a < b),
        Gte => cmp(|a, b| a >= b, |a, b| a >= b),
        Lte => cmp(|a, b| a <= b, |a, b| a <= b),
        Req | Nreq => {
            let re = match &right {
                Value::Pattern(p) => p.clone(),
                Value::Str(s) => Arc::new(
                    GoRegex::compile(s)
                        .map_err(|e| Fault::Error(format!("Unable to compile regexp pattern '{s}': {e}")))?,
                ),
                _ => return Err(Fault::nil()),
            };
            let Value::Str(s) = &left else { return Err(Fault::nil()) };
            Value::Bool(re.is_match(s) == (sym == Req))
        }
        And => Value::Bool(matches!(left, Value::Bool(true)) && matches!(right, Value::Bool(true))),
        Or => Value::Bool(matches!(left, Value::Bool(true)) || matches!(right, Value::Bool(true))),
        In => {
            let Value::List(items) = &right else {
                return Err(Fault::nil());
            };
            for v in items {
                if interface_equal(&left, v)? {
                    return Ok(Value::Bool(true));
                }
            }
            Value::Bool(false)
        }
        BitwiseOr => Value::Num((to_i64(l) | to_i64(r)) as f64),
        BitwiseAnd => Value::Num((to_i64(l) & to_i64(r)) as f64),
        BitwiseXor => Value::Num((to_i64(l) ^ to_i64(r)) as f64),
        BitwiseLshift => Value::Num(to_u64(l).checked_shl(shift(r)).unwrap_or(0) as f64),
        BitwiseRshift => Value::Num(to_u64(l).checked_shr(shift(r)).unwrap_or(0) as f64),
        Plus => match (&left, &right) {
            (Value::Str(_), _) | (_, Value::Str(_)) => Value::Str(format!("{}{}", sprint(&left), sprint(&right))),
            _ => Value::Num(l + r),
        },
        Minus => Value::Num(l - r),
        Multiply => Value::Num(l * r),
        Divide => Value::Num(l / r),
        Modulus => Value::Num(l % r),
        Exponent => Value::Num(go_pow(l, r)),
        Negate => Value::Num(-r),
        Invert => Value::Bool(!matches!(right, Value::Bool(true))),
        BitwiseNot => Value::Num(!to_i64(r) as f64),
        TernaryTrue => match left {
            Value::Bool(true) => right,
            _ => Value::Nil,
        },
        TernaryFalse | Coalesce => match left {
            Value::Nil => right,
            l => l,
        },
        Separate => match left {
            Value::List(mut l) => {
                l.push(right);
                Value::List(l)
            }
            l => Value::List(vec![l, right]),
        },
        Sym::Value | Literal | Noop | Functional | Access => Value::Nil,
    })
}

/// A shift count as Go converts it: uint64, a count past 63 shifting everything out.
fn shift(r: f64) -> u32 {
    u32::try_from(to_u64(r)).unwrap_or(u32::MAX)
}

/// `math.Pow`, whose special cases Rust's `powf` shares but for a base of -1 and an infinite exponent.
fn go_pow(x: f64, y: f64) -> f64 {
    if x == -1.0 && y.is_infinite() {
        return 1.0;
    }
    x.powf(y)
}

// ---------------------------------------------------------------------------------------------------------------
// Accessors (`makeAccessorStage`): a field or a method of a principal or a resource, as reflection reaches it

#[derive(Clone, Copy, PartialEq, Eq)]
enum GoKind {
    Bool,
    Float64,
    String,
    Ptr,
    Slice,
    Struct,
}

impl GoKind {
    fn name(self) -> &'static str {
        match self {
            GoKind::Bool => "bool",
            GoKind::Float64 => "float64",
            GoKind::String => "string",
            GoKind::Ptr => "ptr",
            GoKind::Slice => "slice",
            GoKind::Struct => "struct",
        }
    }
}

fn kind_of(v: &Value) -> Option<GoKind> {
    Some(match v {
        Value::Nil => return None,
        Value::Bool(_) => GoKind::Bool,
        Value::Num(_) => GoKind::Float64,
        Value::Str(_) => GoKind::String,
        Value::Pattern(_) => GoKind::Ptr,
        Value::List(_) | Value::Strings(_) => GoKind::Slice,
        Value::Principal(_) | Value::Resource(_) => GoKind::Struct,
    })
}

/// The parameters of a method of `authz.Principal`, by its name (none for a resource: it has no method).
fn method_params(name: &str) -> Option<&'static [GoKind]> {
    Some(match name {
        "AllRoles" | "Anonymous" | "EndUser" | "OnProject" | "System" => &[],
        "For" | "HasRole" => &[GoKind::String],
        "HasRoleIn" => &[GoKind::String, GoKind::Struct],
        "MayRun" => &[GoKind::Struct],
        _ => return None,
    })
}

fn principal_field(p: &Subject, name: &str) -> Option<Value> {
    Some(match name {
        "Subject" => Value::Str(p.subject.clone()),
        "Org" => Value::Str(p.org.clone()),
        "Project" => Value::Str(p.project.clone()),
        "Roles" => Value::Strings(p.roles.clone()),
        "Global" => Value::Strings(p.global.clone()),
        "ProjectAdmin" => Value::Bool(p.project_admin),
        "ProjectOwner" => Value::Bool(p.project_owner),
        "OnBehalfOf" => Value::Str(p.on_behalf_of.clone()),
        _ => return None,
    })
}

fn resource_field(r: &Resource, name: &str) -> Option<Value> {
    Some(match name {
        "Type" => Value::Str(r.typ.clone()),
        "ID" => Value::Str(r.id.clone()),
        "Org" => Value::Str(r.org.clone()),
        "Namespace" => Value::Str(r.namespace.clone()),
        "Owner" => Value::Str(r.owner.clone()),
        "Name" => Value::Str(r.name.clone()),
        "Role" => Value::Str(r.role.clone()),
        "Accountable" => Value::Str(r.accountable.clone()),
        "OrgChain" => Value::Strings(r.org_chain.clone()),
        "ProjectID" => Value::Str(r.project_id.clone()),
        "Roles" => Value::Strings(r.roles.clone()),
        _ => return None,
    })
}

fn call_method(p: &Subject, name: &str, args: &[Value]) -> Value {
    let s = |i: usize| match args.get(i) {
        Some(Value::Str(s)) => s.clone(),
        _ => String::new(),
    };
    let res = |i: usize| match args.get(i) {
        Some(Value::Resource(r)) => (**r).clone(),
        _ => Resource::default(),
    };
    match name {
        "AllRoles" => {
            let mut out = p.roles.clone();
            for r in &p.global {
                if !out.contains(r) {
                    out.push(r.clone());
                }
            }
            Value::Strings(out)
        }
        "Anonymous" => Value::Bool(p.anonymous()),
        "System" => Value::Bool(p.subject.starts_with("system:")),
        "EndUser" => Value::Str(if p.subject.starts_with("system:") {
            p.on_behalf_of.clone()
        } else {
            p.subject.clone()
        }),
        "For" => {
            let mut q = p.clone();
            let user = s(0);
            if !user.is_empty() && q.subject.starts_with("system:") {
                q.on_behalf_of = user;
            }
            Value::Principal(Box::new(q))
        }
        "HasRole" => Value::Bool(p.has_role(&s(0))),
        "HasRoleIn" => Value::Bool(p.has_role_in(&s(0), &res(1))),
        "OnProject" => Value::Bool(p.on_project()),
        "MayRun" => Value::Bool(p.may_run(&res(0))),
        _ => Value::Nil,
    }
}

fn access(path: &[String], right: Value, host: &dyn Host) -> Result<Value, Fault> {
    let joined = path.join(".");
    let mut value = host.param(&path[0])?;
    for i in 1..path.len() {
        let (name, prev) = (&path[i], &path[i - 1]);
        let field = match &value {
            Value::Principal(_) | Value::Resource(_) => {
                let first = name.chars().next().unwrap_or('\0');
                let mut up = first.to_uppercase();
                let upper = match (up.next(), up.next()) {
                    (Some(u), None) => u,
                    _ => first,
                };
                if upper != first {
                    return err(format!("Unable to access unexported field '{name}' in '{prev}'"));
                }
                match &value {
                    Value::Principal(p) => principal_field(p, name),
                    Value::Resource(r) => resource_field(r, name),
                    _ => None,
                }
            }
            _ => return err(format!("Unable to access '{name}', '{prev}' is not a struct or map")),
        };
        if let Some(f) = field {
            value = f;
            continue;
        }
        let params = match &value {
            Value::Principal(_) => method_params(name),
            _ => None,
        };
        let Some(params) = params else {
            return err(format!("No method or field '{name}' present on parameter '{prev}'"));
        };
        let args = match &right {
            Value::List(l) => l.clone(),
            Value::Nil => Vec::new(),
            v => vec![v.clone()],
        };
        let failed = |e: String| Fault::Error(format!("Method call failed - '{}.{}': {e}", path[0], path[1]));
        if params.len() != args.len() {
            let which = if params.len() > args.len() { "few" } else { "many" };
            return Err(failed(format!(
                "Too {which} arguments to parameter call: got {} arguments, expected {}",
                args.len(),
                params.len()
            )));
        }
        for (want, a) in params.iter().zip(&args) {
            let Some(k) = kind_of(a) else {
                // reflect.Value.Type of a zero Value panics with an error, which the accessor's recover cannot read
                return Err(Fault::Panic(
                    "interface conversion: interface {} is *reflect.ValueError, not string".into(),
                ));
            };
            if k != *want {
                return Err(failed(format!(
                    "Argument type conversion failed: failed to convert '{}' to '{}'",
                    k.name(),
                    want.name()
                )));
            }
            if k == GoKind::Struct && !matches!(a, Value::Resource(_)) {
                return err(format!(
                    "Failed to access '{joined}': reflect: Call using authz.Principal as type authz.Resource"
                ));
            }
        }
        let Value::Principal(p) = &value else {
            return Err(Fault::nil());
        };
        value = call_method(p, name, &args);
    }
    Ok(value)
}

#[cfg(test)]
mod tests {
    use super::*;

    struct NoHost;

    impl Host for NoHost {
        fn is_function(&self, _: &str) -> bool {
            false
        }

        fn call(&self, _: &str, _: Vec<Value>) -> Result<Value, Fault> {
            err("no function")
        }

        fn param(&self, name: &str) -> Result<Value, Fault> {
            err(format!("No parameter '{name}' found."))
        }
    }

    fn run(e: &str) -> String {
        match Expression::new(e, &NoHost).and_then(|x| x.eval(&NoHost)) {
            Ok(v) => sprint(&v),
            Err(Fault::Error(e)) => format!("error: {e}"),
            Err(Fault::Panic(e)) => format!("panic: {e}"),
        }
    }

    #[test]
    fn evaluates_as_govaluate() {
        assert_eq!(run("1 + 2 * 3"), "7");
        assert_eq!(run("10 - 2 - 3"), "5");
        assert_eq!(run("2 ** 3 ** 2"), "64");
        assert_eq!(run("'a' + 1"), "a1");
        assert_eq!(run("1 == 1 && 2 > 1"), "true");
        assert_eq!(run("true ? 'y' : 'n'"), "y");
        assert_eq!(run("false ? 'y' : 'n'"), "n");
        assert_eq!(run("'b' in ('a', 'b')"), "true");
        assert_eq!(run("'a' in ('a')"), "true");
        assert_eq!(run("'abc' =~ '^a'"), "true");
        assert_eq!(run("1000000 + 0"), "1e+06");
        assert_eq!(
            run("1 && true"),
            "error: Value '1' cannot be used with the logical operator '&&', it is not a bool"
        );
        assert_eq!(
            run("'a' > 1"),
            "error: Value 'a' cannot be used with the comparator '>', it is not a number"
        );
        assert_eq!(run("(1"), "error: Unbalanced parenthesis");
        assert_eq!(run("1 =="), "error: Unexpected end of expression");
        assert_eq!(run("'a"), "error: Unclosed string literal");
        assert_eq!(run("a &&! b"), "error: Invalid token: '&&!'");
        assert_eq!(run("x"), "error: No parameter 'x' found.");
        assert_eq!(run("f(1)"), "error: Undefined function f");
        assert_eq!(
            run("1 2"),
            "error: Cannot transition token types from NUMERIC [1] to NUMERIC [2]"
        );
        assert_eq!(run("'2006-01-02' == 1136160000"), "true");
    }

    #[test]
    fn prints_floats_as_go() {
        for (f, s) in [
            (1e6, "1e+06"),
            (123456.0, "123456"),
            (123456789.0, "1.23456789e+08"),
            (1e-4, "0.0001"),
        ] {
            assert_eq!(go_float(f), s);
        }
        assert_eq!(go_float(1e-5), "1e-05");
        assert_eq!(go_float(0.5), "0.5");
    }
}
