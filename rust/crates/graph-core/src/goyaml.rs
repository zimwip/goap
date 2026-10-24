//! YAML written as Go's `gopkg.in/yaml.v3` encoder writes it (block style, an indent of 2, no line wrapping): the
//! style of each scalar chosen by the encoder (`encoder.stringv`, `resolve`), then checked by the emitter
//! (`yaml_emitter_analyze_scalar`, `yaml_emitter_select_scalar_style`), and the layout of the emitter's state machine
//! (`increase_indent`, `write_indent`, `write_indicator`) for what the graph writes: mappings, sequences and scalars,
//! with no comment, anchor or tag.

use serde_json::Value as Json;
use std::cmp::Ordering;

/// A value to write.
#[derive(Debug, Clone, PartialEq)]
pub enum Yaml {
    /// a string, its style chosen as `encoder.stringv` does
    Str(String),
    /// a string written as a key of a `yaml.Node` mapping (`Tag: "!!str"`, no style): quoted only when it would
    /// resolve to another tag, multi-line literal
    NodeKey(String),
    /// a string a `yaml.Node` holds in the literal style (read back from a literal)
    Literal(String),
    /// a number, a boolean, a null or a time: plain text
    Plain(String),
    Seq(Vec<Yaml>),
    /// a mapping, its entries in order
    Map(Vec<(Yaml, Yaml)>),
}

impl Yaml {
    pub fn str(s: impl Into<String>) -> Yaml {
        Yaml::Str(s.into())
    }

    pub fn int(i: i64) -> Yaml {
        Yaml::Plain(i.to_string())
    }

    /// A JSON value as Go's encoder writes the `any` it decodes to: numbers as `float64`, objects as maps with their
    /// keys sorted as `yaml.v3` sorts them.
    pub fn of_json(v: &Json) -> Yaml {
        match v {
            Json::Null => Yaml::Plain("null".into()),
            Json::Bool(b) => Yaml::Plain(b.to_string()),
            Json::Number(n) => Yaml::Plain(float_g(n.as_f64().unwrap_or(0.0))),
            Json::String(s) => Yaml::Str(s.clone()),
            Json::Array(a) => Yaml::Seq(a.iter().map(Yaml::of_json).collect()),
            Json::Object(m) => {
                let mut keys: Vec<&String> = m.keys().collect();
                keys.sort_by(|a, b| key_order(a, b));
                Yaml::Map(
                    keys.into_iter()
                        .map(|k| (Yaml::Str(k.clone()), Yaml::of_json(&m[k])))
                        .collect(),
                )
            }
        }
    }
}

/// A value as `yaml.Node.Encode` holds it, which writes it then reads it back: a literal string starting with a line
/// break loses that break, and one with an indentation hint below a sequence does not read back (Go's error).
pub fn round_trip(v: Yaml) -> Result<Yaml, String> {
    fn walk(v: Yaml, in_seq: bool) -> Result<Yaml, String> {
        Ok(match v {
            Yaml::Str(s) if s.contains('\n') && analyze(&s).block => {
                let b = s.as_bytes();
                let hinted = b[0] == b' ' || is_break(b, 0);
                if hinted
                    && in_seq
                    && s.chars()
                        .any(|c| !matches!(c, '\n' | '\u{85}' | '\u{2028}' | '\u{2029}'))
                {
                    return Err("yaml: line 1: did not find expected '-' indicator".into());
                }
                if is_break(b, 0) {
                    Yaml::Literal(s[width(b[0]).max(1)..].to_string())
                } else {
                    Yaml::Str(s)
                }
            }
            Yaml::Seq(items) => Yaml::Seq(items.into_iter().map(|i| walk(i, true)).collect::<Result<_, _>>()?),
            Yaml::Map(entries) => Yaml::Map(
                entries
                    .into_iter()
                    .map(|(k, v)| Ok((walk(k, in_seq)?, walk(v, in_seq)?)))
                    .collect::<Result<_, String>>()?,
            ),
            v => v,
        })
    }
    walk(v, false)
}

/// A float as Go's `strconv.FormatFloat(f, 'g', -1, 64)` writes it, infinities and NaN as YAML's.
pub fn float_g(f: f64) -> String {
    if f.is_nan() {
        return ".nan".into();
    }
    if f.is_infinite() {
        return if f > 0.0 { ".inf".into() } else { "-.inf".into() };
    }
    if f == 0.0 {
        return if f.is_sign_negative() { "-0".into() } else { "0".into() };
    }
    // the shortest digits that read back, and the exponent of the first
    let e = format!("{:e}", f.abs());
    let (mant, exp) = e.split_once('e').unwrap_or((&e, "0"));
    let exp: i32 = exp.parse().unwrap_or(0);
    let digits: Vec<u8> = mant.bytes().filter(u8::is_ascii_digit).collect();
    let nd = digits.len() as i32;
    let dp = exp + 1;
    let mut out = String::new();
    if f < 0.0 {
        out.push('-');
    }
    if !(-4..6).contains(&exp) {
        out.push(digits[0] as char);
        if nd > 1 {
            out.push('.');
            out.extend(digits[1..].iter().map(|&d| d as char));
        }
        out.push('e');
        out.push(if exp < 0 { '-' } else { '+' });
        let x = exp.unsigned_abs();
        if x < 10 {
            out.push('0');
        }
        out.push_str(&x.to_string());
        return out;
    }
    let digit = |i: i32| {
        if (0..nd).contains(&i) {
            digits[i as usize] as char
        } else {
            '0'
        }
    };
    if dp > 0 {
        (0..dp).for_each(|i| out.push(digit(i)));
    } else {
        out.push('0');
    }
    let prec = (nd - dp).max(0);
    if prec > 0 {
        out.push('.');
        (1..=prec).for_each(|i| out.push(digit(dp + i - 1)));
    }
    out
}

/// The order `yaml.v3` gives the string keys of a map (`keyList.Less`): runs of digits compared by their value,
/// letters before other characters.
pub fn key_order(a: &str, b: &str) -> Ordering {
    if key_less(a, b) {
        Ordering::Less
    } else if key_less(b, a) {
        Ordering::Greater
    } else {
        Ordering::Equal
    }
}

fn key_less(a: &str, b: &str) -> bool {
    let (ar, br): (Vec<char>, Vec<char>) = (a.chars().collect(), b.chars().collect());
    let is_digit = |c: char| c.is_ascii_digit() || (!c.is_ascii() && c.is_numeric());
    let mut digits = false;
    let n = ar.len().min(br.len());
    for i in 0..n {
        if ar[i] == br[i] {
            digits = is_digit(ar[i]);
            continue;
        }
        let (al, bl) = (ar[i].is_alphabetic(), br[i].is_alphabetic());
        if al && bl {
            return ar[i] < br[i];
        }
        if al || bl {
            return if digits { al } else { bl };
        }
        let (mut an, mut bn) = (0i64, 0i64);
        if ar[i] == '0' || br[i] == '0' {
            for j in (0..i).rev() {
                if !is_digit(ar[j]) {
                    break;
                }
                if ar[j] != '0' {
                    an = 1;
                    bn = 1;
                    break;
                }
            }
        }
        let mut ai = i;
        while ai < ar.len() && is_digit(ar[ai]) {
            an = an.wrapping_mul(10).wrapping_add(ar[ai] as i64 - '0' as i64);
            ai += 1;
        }
        let mut bi = i;
        while bi < br.len() && is_digit(br[bi]) {
            bn = bn.wrapping_mul(10).wrapping_add(br[bi] as i64 - '0' as i64);
            bi += 1;
        }
        if an != bn {
            return an < bn;
        }
        if ai != bi {
            return ai < bi;
        }
        return ar[i] < br[i];
    }
    ar.len() < br.len()
}

/// The tag a plain scalar resolves to (`resolve("", s)`): only whether it is a string matters here.
fn resolves_to_str(s: &str) -> bool {
    let Some(&c) = s.as_bytes().first() else {
        return false; // null
    };
    let hint = match c {
        b'+' | b'-' | b'0'..=b'9' => 'D',
        b'y' | b'Y' | b'n' | b'N' | b't' | b'T' | b'f' | b'F' | b'o' | b'O' | b'~' => 'M',
        b'.' => '.',
        _ => return true,
    };
    if matches!(
        s,
        "true"
            | "True"
            | "TRUE"
            | "false"
            | "False"
            | "FALSE"
            | "~"
            | "null"
            | "Null"
            | "NULL"
            | ".nan"
            | ".NaN"
            | ".NAN"
            | ".inf"
            | ".Inf"
            | ".INF"
            | "+.inf"
            | "+.Inf"
            | "+.INF"
            | "-.inf"
            | "-.Inf"
            | "-.INF"
            | "<<"
    ) {
        return false;
    }
    match hint {
        'M' => true,
        '.' => !go_float(s),
        _ => {
            if is_timestamp(s) {
                return false;
            }
            let plain = s.replace('_', "");
            if go_int(&plain) || yaml_float(&plain) && go_float(&plain) {
                return false;
            }
            for (p, radix) in [("0b", 2), ("-0b", 2), ("0o", 8), ("-0o", 8)] {
                if let Some(rest) = plain.strip_prefix(p) {
                    let neg = p.starts_with('-');
                    let ok = !rest.is_empty()
                        && rest.bytes().all(|c| (c as char).is_digit(radix))
                        && (u64::from_str_radix(rest, radix).is_ok() && !neg
                            || i64::from_str_radix(&format!("-{rest}"), radix).is_ok());
                    if ok {
                        return false;
                    }
                }
            }
            true
        }
    }
}

/// Whether Go's `strconv.ParseInt(s, 0, 64)` or `ParseUint(s, 0, 64)` reads `s`.
fn go_int(s: &str) -> bool {
    let (neg, body) = match s.as_bytes().first() {
        Some(b'+') => (false, &s[1..]),
        Some(b'-') => (true, &s[1..]),
        _ => (false, s),
    };
    let lower = body.to_ascii_lowercase();
    let (radix, digits) = if let Some(d) = lower.strip_prefix("0x") {
        (16, d)
    } else if let Some(d) = lower.strip_prefix("0o") {
        (8, d)
    } else if let Some(d) = lower.strip_prefix("0b") {
        (2, d)
    } else if lower.len() > 1 && lower.starts_with('0') {
        (8, &lower[1..])
    } else {
        (10, lower.as_str())
    };
    if digits.is_empty() || !digits.chars().all(|c| c.is_digit(radix)) {
        return false;
    }
    match u128::from_str_radix(digits, radix) {
        Ok(v) if neg => v <= 1u128 << 63,
        Ok(v) => v <= u64::MAX as u128,
        Err(_) => false,
    }
}

/// `^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`
fn yaml_float(s: &str) -> bool {
    let b = s.as_bytes();
    let mut i = usize::from(matches!(b.first(), Some(b'+' | b'-')));
    let digits = |i: &mut usize| {
        let from = *i;
        while *i < b.len() && b[*i].is_ascii_digit() {
            *i += 1;
        }
        *i - from
    };
    if b.get(i) == Some(&b'.') {
        i += 1;
        if digits(&mut i) == 0 {
            return false;
        }
    } else {
        if digits(&mut i) == 0 {
            return false;
        }
        if b.get(i) == Some(&b'.') {
            i += 1;
            digits(&mut i);
        }
    }
    if matches!(b.get(i), Some(b'e' | b'E')) {
        i += 1;
        if matches!(b.get(i), Some(b'+' | b'-')) {
            i += 1;
        }
        if digits(&mut i) == 0 {
            return false;
        }
    }
    i == b.len()
}

/// Whether Go's `strconv.ParseFloat(s, 64)` reads a decimal `s` with no error (an overflow is one).
fn go_float(s: &str) -> bool {
    let body = s.trim_start_matches(['+', '-']);
    if s.len() - body.len() > 1 {
        return false;
    }
    let lower = body.to_ascii_lowercase();
    if matches!(lower.as_str(), "inf" | "infinity" | "nan") {
        return true;
    }
    yaml_float(body) && s.parse::<f64>().is_ok_and(f64::is_finite)
}

/// Whether `s` reads as one of the timestamps `yaml.v3` accepts (`parseTimestamp`).
fn is_timestamp(s: &str) -> bool {
    let b = s.as_bytes();
    if b.len() < 5 || !b[..4].iter().all(u8::is_ascii_digit) || b[4] != b'-' {
        return false;
    }
    let mut p = Reader { b, i: 5 };
    let year: u32 = s[..4].parse().unwrap_or(0);
    let (Some(month), true) = (p.num(), p.eat(b'-')) else {
        return false;
    };
    let Some(day) = p.num() else {
        return false;
    };
    if !(1..=12).contains(&month) || day < 1 || day > days_in(year, month) {
        return false;
    }
    if p.done() {
        return true;
    }
    let sep = b[p.i];
    if !matches!(sep, b'T' | b't' | b' ') {
        return false;
    }
    p.i += 1;
    let (Some(h), true, Some(m), true, Some(sec)) = (p.num(), p.eat(b':'), p.num(), p.eat(b':'), p.num()) else {
        return false;
    };
    if h > 23 || m > 59 || sec > 59 {
        return false;
    }
    if p.eat(b'.') && p.digits() == 0 {
        return false;
    }
    if sep == b' ' {
        return p.done();
    }
    if p.eat(b'Z') {
        return p.done();
    }
    if !(p.eat(b'+') || p.eat(b'-')) {
        return false;
    }
    let (Some(zh), true, Some(zm)) = (p.fixed2(), p.eat(b':'), p.fixed2()) else {
        return false;
    };
    zh <= 24 && zm <= 59 && p.done()
}

struct Reader<'a> {
    b: &'a [u8],
    i: usize,
}

impl Reader<'_> {
    fn done(&self) -> bool {
        self.i == self.b.len()
    }
    fn eat(&mut self, c: u8) -> bool {
        let ok = self.b.get(self.i) == Some(&c);
        self.i += usize::from(ok);
        ok
    }
    fn digits(&mut self) -> usize {
        let from = self.i;
        while self.b.get(self.i).is_some_and(u8::is_ascii_digit) {
            self.i += 1;
        }
        self.i - from
    }
    /// one or two digits
    fn num(&mut self) -> Option<u32> {
        let from = self.i;
        let mut n = 0;
        while n < 2 && self.b.get(self.i).is_some_and(u8::is_ascii_digit) {
            self.i += 1;
            n += 1;
        }
        (n > 0).then(|| std::str::from_utf8(&self.b[from..self.i]).ok()?.parse().ok())?
    }
    fn fixed2(&mut self) -> Option<u32> {
        let from = self.i;
        (self.digits() == 2).then(|| std::str::from_utf8(&self.b[from..self.i]).ok()?.parse().ok())?
    }
}

fn days_in(year: u32, month: u32) -> u32 {
    match month {
        2 if year.is_multiple_of(4) && (!year.is_multiple_of(100) || year.is_multiple_of(400)) => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

/// `isBase60Float`: `^[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+(?:\.[0-9_]*)?$`
fn base60_float(s: &str) -> bool {
    let b = s.as_bytes();
    if !s.contains(':') {
        return false;
    }
    let mut i = usize::from(matches!(b.first(), Some(b'+' | b'-')));
    if !b.get(i).is_some_and(u8::is_ascii_digit) {
        return false;
    }
    while b.get(i).is_some_and(|c| c.is_ascii_digit() || *c == b'_') {
        i += 1;
    }
    let mut groups = 0;
    while b.get(i) == Some(&b':') {
        i += 1;
        let from = i;
        while i - from < 2 && b.get(i).is_some_and(u8::is_ascii_digit) {
            i += 1;
        }
        // [0-5]?[0-9]: one digit, or two with the first at most 5
        match i - from {
            1 => {}
            2 if b[from] <= b'5' => {}
            2 => i -= 1, // the regexp backtracks to one digit: then a `:` or `.` must follow, which fails below
            _ => return false,
        }
        groups += 1;
    }
    if groups == 0 {
        return false;
    }
    if b.get(i) == Some(&b'.') {
        i += 1;
        while b.get(i).is_some_and(|c| c.is_ascii_digit() || *c == b'_') {
            i += 1;
        }
    }
    i == b.len()
}

fn old_bool(s: &str) -> bool {
    matches!(
        s,
        "y" | "Y" | "yes" | "Yes" | "YES" | "on" | "On" | "ON" | "n" | "N" | "no" | "No" | "NO" | "off" | "Off" | "OFF"
    )
}

#[derive(Debug, Clone, Copy, PartialEq)]
enum Style {
    Plain,
    Single,
    Double,
    Literal,
}

/// What the emitter's analysis allows of a scalar (`yaml_emitter_analyze_scalar`, unicode on).
struct Analysis {
    multiline: bool,
    block_plain: bool,
    single: bool,
    block: bool,
}

fn width(c: u8) -> usize {
    match c {
        _ if c & 0x80 == 0 => 1,
        _ if c & 0xE0 == 0xC0 => 2,
        _ if c & 0xF0 == 0xE0 => 3,
        _ if c & 0xF8 == 0xF0 => 4,
        _ => 0,
    }
}

fn at(b: &[u8], i: usize) -> u8 {
    b.get(i).copied().unwrap_or(0)
}

fn is_break(b: &[u8], i: usize) -> bool {
    let c = at(b, i);
    c == b'\r'
        || c == b'\n'
        || c == 0xC2 && at(b, i + 1) == 0x85
        || c == 0xE2 && at(b, i + 1) == 0x80 && matches!(at(b, i + 2), 0xA8 | 0xA9)
}

fn is_blankz(b: &[u8], i: usize) -> bool {
    matches!(at(b, i), b' ' | b'\t' | 0) || is_break(b, i)
}

fn is_printable(b: &[u8], i: usize) -> bool {
    let (c, d, e) = (at(b, i), at(b, i + 1), at(b, i + 2));
    c == 0x0A
        || (0x20..=0x7E).contains(&c)
        || c == 0xC2 && d >= 0xA0
        || c > 0xC2 && c < 0xED
        || c == 0xED && d < 0xA0
        || c == 0xEE
        || c == 0xEF && !(d == 0xBB && e == 0xBF) && !(d == 0xBF && (e == 0xBE || e == 0xBF))
}

fn analyze(value: &str) -> Analysis {
    let b = value.as_bytes();
    if b.is_empty() {
        return Analysis {
            multiline: false,
            block_plain: true,
            single: true,
            block: false,
        };
    }
    let mut block_indicators = b.len() >= 3 && (b.starts_with(b"---") || b.starts_with(b"..."));
    let (mut line_breaks, mut special, mut tabs) = (false, false, false);
    let (mut leading_space, mut leading_break, mut trailing_space, mut trailing_break) = (false, false, false, false);
    let (mut break_space, mut space_break) = (false, false);
    let (mut previous_space, mut previous_break) = (false, false);
    let mut preceded_by_whitespace = true;
    let mut i = 0;
    while i < b.len() {
        let w = width(b[i]).max(1);
        let followed_by_whitespace = i + w >= b.len() || matches!(b[i + w], b' ' | b'\t');
        if i == 0 {
            match b[i] {
                b'#' | b',' | b'[' | b']' | b'{' | b'}' | b'&' | b'*' | b'!' | b'|' | b'>' | b'\'' | b'"' | b'%'
                | b'@' | b'`' => block_indicators = true,
                b'?' | b':' if followed_by_whitespace => block_indicators = true,
                b'-' if followed_by_whitespace => block_indicators = true,
                _ => {}
            }
        } else {
            match b[i] {
                b':' if followed_by_whitespace => block_indicators = true,
                b'#' if preceded_by_whitespace => block_indicators = true,
                _ => {}
            }
        }
        if b[i] == b'\t' {
            tabs = true;
        } else if !is_printable(b, i) {
            special = true;
        }
        if b[i] == b' ' {
            leading_space |= i == 0;
            trailing_space |= i + w == b.len();
            break_space |= previous_break;
            previous_space = true;
            previous_break = false;
        } else if is_break(b, i) {
            line_breaks = true;
            leading_break |= i == 0;
            trailing_break |= i + w == b.len();
            space_break |= previous_space;
            previous_space = false;
            previous_break = true;
        } else {
            previous_space = false;
            previous_break = false;
        }
        preceded_by_whitespace = is_blankz(b, i);
        i += w;
    }
    let mut a = Analysis {
        multiline: line_breaks,
        block_plain: true,
        single: true,
        block: true,
    };
    if leading_space || leading_break || trailing_space || trailing_break {
        a.block_plain = false;
    }
    if trailing_space {
        a.block = false;
    }
    if break_space {
        a.block_plain = false;
        a.single = false;
    }
    if space_break || tabs || special {
        a.block_plain = false;
        a.single = false;
    }
    if space_break || special {
        a.block = false;
    }
    if line_breaks || block_indicators {
        a.block_plain = false;
    }
    a
}

/// Writes a document as `yaml.v3`'s `Encoder` with `SetIndent(2)` does, then closed.
pub fn to_string(doc: &Yaml) -> String {
    to_string_indent(doc, 2)
}

/// Writes a document as `yaml.v3`'s `Encoder` with `SetIndent(indent)` does (`yaml.Marshal`: 4), then closed.
pub fn to_string_indent(doc: &Yaml, indent: i64) -> String {
    let mut e = Emitter {
        best_indent: indent,
        out: String::new(),
        column: 0,
        whitespace: true,
        indention: true,
        open_ended: false,
        indent: -1,
        indents: Vec::new(),
    };
    e.node(doc, Ctx::Root);
    // the document end, then the stream end
    e.indent = 0;
    e.write_indent();
    if e.open_ended {
        e.indicator("...", true, false, false);
        e.write_indent();
    }
    e.out
}

/// Whether a key is written as a simple key (`yaml_emitter_check_simple_key`): a scalar on one line of at most 128
/// bytes, or an empty collection.
fn simple_key(k: &Yaml) -> bool {
    match k {
        Yaml::Str(s) | Yaml::NodeKey(s) | Yaml::Literal(s) | Yaml::Plain(s) => !analyze(s).multiline && s.len() <= 128,
        Yaml::Seq(i) => i.is_empty(),
        Yaml::Map(e) => e.is_empty(),
    }
}

/// Where a node is written: the state the emitter pushed before it.
#[derive(Clone, Copy, PartialEq)]
enum Ctx {
    Root,
    /// a key of a block mapping
    Key,
    /// a value of a block mapping
    Value,
    /// an item of a block sequence
    Item,
}

struct Emitter {
    /// the indentation chosen (`best_indent`)
    best_indent: i64,
    out: String,
    column: i64,
    whitespace: bool,
    indention: bool,
    open_ended: bool,
    indent: i64,
    indents: Vec<i64>,
}

impl Emitter {
    fn put(&mut self, c: char) {
        self.out.push(c);
        self.column += 1;
    }

    fn put_break(&mut self) {
        self.out.push('\n');
        self.column = 0;
        self.indention = true;
    }

    fn increase_indent(&mut self, flow: bool, ctx: Ctx) {
        self.indents.push(self.indent);
        if self.indent < 0 {
            self.indent = if flow { self.best_indent } else { 0 };
        } else if ctx == Ctx::Item {
            // the first indent inside a sequence skips the "- " indicator
            self.indent += 2;
        } else {
            let b = self.best_indent;
            self.indent = b * ((self.indent + b) / b);
        }
    }

    fn pop_indent(&mut self) {
        self.indent = self.indents.pop().unwrap_or(-1);
    }

    fn write_indent(&mut self) {
        let indent = self.indent.max(0);
        if !self.indention || self.column > indent || (self.column == indent && !self.whitespace) {
            self.put_break();
        }
        while self.column < indent {
            self.put(' ');
        }
        self.whitespace = true;
    }

    fn indicator(&mut self, s: &str, need_whitespace: bool, is_whitespace: bool, is_indention: bool) {
        if need_whitespace && !self.whitespace {
            self.put(' ');
        }
        for c in s.chars() {
            self.put(c);
        }
        self.whitespace = is_whitespace;
        self.indention = self.indention && is_indention;
        self.open_ended = false;
    }

    fn node(&mut self, n: &Yaml, ctx: Ctx) {
        match n {
            Yaml::Seq(items) if items.is_empty() => self.empty("[", "]"),
            Yaml::Map(entries) if entries.is_empty() => self.empty("{", "}"),
            Yaml::Seq(items) => {
                self.increase_indent(false, ctx);
                for item in items {
                    self.write_indent();
                    self.indicator("-", true, false, true);
                    self.node(item, Ctx::Item);
                }
                self.pop_indent();
            }
            Yaml::Map(entries) => {
                self.increase_indent(false, ctx);
                for (k, v) in entries {
                    self.write_indent();
                    if simple_key(k) {
                        self.node(k, Ctx::Key);
                        self.indicator(":", false, false, false);
                    } else {
                        self.indicator("?", true, false, true);
                        self.node(k, Ctx::Value);
                        self.write_indent();
                        self.indicator(":", true, false, true);
                    }
                    self.node(v, Ctx::Value);
                }
                self.pop_indent();
            }
            Yaml::Str(s) | Yaml::NodeKey(s) if s == "<<" => self.plain("!!merge <<"),
            Yaml::Literal(s) => self.scalar(s, Style::Literal, ctx),
            Yaml::Str(s) => {
                let plain = resolves_to_str(s) && !base60_float(s) && !old_bool(s);
                let style = if s.contains('\n') {
                    Style::Literal
                } else if plain {
                    Style::Plain
                } else {
                    Style::Double
                };
                self.scalar(s, style, ctx);
            }
            Yaml::NodeKey(s) => {
                let style = if s.contains('\n') {
                    Style::Literal
                } else if resolves_to_str(s) {
                    Style::Plain
                } else {
                    Style::Double
                };
                self.scalar(s, style, ctx);
            }
            Yaml::Plain(s) => self.scalar(s, Style::Plain, ctx),
        }
    }

    /// An empty collection, in flow style.
    fn empty(&mut self, open: &str, close: &str) {
        self.indicator(open, true, true, false);
        self.indicator(close, false, false, false);
    }

    fn scalar(&mut self, value: &str, style: Style, ctx: Ctx) {
        let a = analyze(value);
        let simple_key = ctx == Ctx::Key;
        let mut style = style;
        if simple_key && a.multiline {
            style = Style::Double;
        }
        if style == Style::Plain && (!a.block_plain || value.is_empty() && simple_key) {
            style = Style::Single;
        }
        if style == Style::Single && !a.single {
            style = Style::Double;
        }
        if style == Style::Literal && (!a.block || simple_key) {
            style = Style::Double;
        }
        self.increase_indent(true, ctx);
        match style {
            Style::Plain => self.plain(value),
            Style::Single => self.single(value),
            Style::Double => self.double(value),
            Style::Literal => self.literal(value),
        }
        self.pop_indent();
    }

    fn write_char(&mut self, b: &[u8], i: &mut usize) {
        let w = width(b[*i]).max(1);
        self.out
            .push_str(std::str::from_utf8(&b[*i..*i + w]).unwrap_or("\u{FFFD}"));
        self.column += 1;
        *i += w;
    }

    fn write_break(&mut self, b: &[u8], i: &mut usize) {
        if b[*i] == b'\n' {
            self.put_break();
            *i += 1;
        } else {
            self.write_char(b, i);
            self.column = 0;
            self.indention = true;
        }
    }

    fn plain(&mut self, value: &str) {
        if !value.is_empty() && !self.whitespace {
            self.put(' ');
        }
        self.out.push_str(value);
        self.column += value.chars().count() as i64;
        if !value.is_empty() {
            self.whitespace = false;
        }
        self.indention = false;
    }

    fn single(&mut self, value: &str) {
        self.indicator("'", true, false, false);
        let b = value.as_bytes();
        let mut breaks = false;
        let mut i = 0;
        while i < b.len() {
            if b[i] == b' ' {
                self.write_char(b, &mut i);
            } else if is_break(b, i) {
                if !breaks && b[i] == b'\n' {
                    self.put_break();
                }
                self.write_break(b, &mut i);
                breaks = true;
            } else {
                if breaks {
                    self.write_indent();
                }
                if b[i] == b'\'' {
                    self.put('\'');
                }
                self.write_char(b, &mut i);
                self.indention = false;
                breaks = false;
            }
        }
        self.indicator("'", false, false, false);
        self.whitespace = false;
        self.indention = false;
    }

    fn double(&mut self, value: &str) {
        self.indicator("\"", true, false, false);
        let b = value.as_bytes();
        let mut i = 0;
        while i < b.len() {
            let bom = b[i..].starts_with(&[0xEF, 0xBB, 0xBF]);
            if !is_printable(b, i) || bom || is_break(b, i) || b[i] == b'"' || b[i] == b'\\' {
                let w = width(b[i]).max(1);
                let v = std::str::from_utf8(&b[i..i + w])
                    .ok()
                    .and_then(|s| s.chars().next())
                    .map_or(b[i] as u32, |c| c as u32);
                i += w;
                self.put('\\');
                match v {
                    0x00 => self.put('0'),
                    0x07 => self.put('a'),
                    0x08 => self.put('b'),
                    0x09 => self.put('t'),
                    0x0A => self.put('n'),
                    0x0B => self.put('v'),
                    0x0C => self.put('f'),
                    0x0D => self.put('r'),
                    0x1B => self.put('e'),
                    0x22 => self.put('"'),
                    0x5C => self.put('\\'),
                    0x85 => self.put('N'),
                    0xA0 => self.put('_'),
                    0x2028 => self.put('L'),
                    0x2029 => self.put('P'),
                    _ => {
                        let (c, n) = if v <= 0xFF {
                            ('x', 2)
                        } else if v <= 0xFFFF {
                            ('u', 4)
                        } else {
                            ('U', 8)
                        };
                        self.put(c);
                        for k in (0..n).rev() {
                            let d = (v >> (k * 4)) & 0xF;
                            self.put(char::from_digit(d, 16).unwrap_or('0').to_ascii_uppercase());
                        }
                    }
                }
            } else {
                self.write_char(b, &mut i);
            }
        }
        self.indicator("\"", false, false, false);
        self.whitespace = false;
        self.indention = false;
    }

    fn literal(&mut self, value: &str) {
        self.indicator("|", true, false, false);
        let b = value.as_bytes();
        // the block scalar hints
        if b.first() == Some(&b' ') || is_break(b, 0) {
            let hint = self.best_indent.to_string();
            self.indicator(&hint, false, false, false);
        }
        // the chomping hint (its indicator clears the open end the `+` hint sets)
        let mut last = b.len() - 1;
        while b[last] & 0xC0 == 0x80 {
            last -= 1;
        }
        let chomp = if !is_break(b, last) {
            Some("-")
        } else if last == 0 {
            Some("+")
        } else {
            let mut j = last - 1;
            while b[j] & 0xC0 == 0x80 {
                j -= 1;
            }
            is_break(b, j).then_some("+")
        };
        if let Some(c) = chomp {
            self.indicator(c, false, false, false);
        }
        self.whitespace = true;
        let mut breaks = true;
        let mut i = 0;
        while i < b.len() {
            if is_break(b, i) {
                self.write_break(b, &mut i);
                breaks = true;
            } else {
                if breaks {
                    self.write_indent();
                }
                self.write_char(b, &mut i);
                self.indention = false;
                breaks = false;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn map(entries: Vec<(&str, Yaml)>) -> Yaml {
        Yaml::Map(entries.into_iter().map(|(k, v)| (Yaml::NodeKey(k.into()), v)).collect())
    }

    #[test]
    fn writes_as_yaml_v3() {
        let doc = map(vec![
            ("key", Yaml::str("REQ-1")),
            ("title", Yaml::str("a: b")),
            ("aliases", Yaml::Seq(vec![Yaml::str("REQ-1")])),
            ("tags", Yaml::Seq(vec![])),
            ("verifies", Yaml::Seq(vec![Yaml::str("[[Test/T-1|A test]]")])),
            ("yes", Yaml::str("yes")),
            ("date", Yaml::str("2024-01-02")),
            ("n", Yaml::Plain(float_g(1_000_000.0))),
            ("text", Yaml::str("one\ntwo\n")),
            ("trail", Yaml::str("one\ntwo")),
            ("quote", Yaml::str("it's\t")),
            (
                "linkProperties",
                Yaml::Seq(vec![Yaml::Map(vec![
                    (Yaml::str("type"), Yaml::str("alm@verifies")),
                    (
                        Yaml::str("properties"),
                        Yaml::Map(vec![(Yaml::str("w"), Yaml::Plain("2".into()))]),
                    ),
                ])]),
            ),
        ]);
        assert_eq!(
            to_string(&doc),
            "key: REQ-1\ntitle: 'a: b'\naliases:\n  - REQ-1\ntags: []\nverifies:\n  - '[[Test/T-1|A test]]'\n\
             yes: \"yes\"\ndate: \"2024-01-02\"\nn: 1e+06\ntext: |\n  one\n  two\ntrail: |-\n  one\n  two\n\
             quote: \"it's\\t\"\nlinkProperties:\n  - type: alm@verifies\n    properties:\n      w: 2\n"
        );
    }

    #[test]
    fn formats_floats_as_go() {
        for (f, s) in [
            (3.0, "3"),
            (0.5, "0.5"),
            (123456.0, "123456"),
            (1234567.0, "1.234567e+06"),
            (0.0001, "0.0001"),
            (0.00001, "1e-05"),
            (-2.5, "-2.5"),
        ] {
            assert_eq!(float_g(f), s);
        }
    }

    #[test]
    fn sorts_keys_as_yaml_v3() {
        let mut k = vec!["a10", "a2", "b", "A", "_x"];
        k.sort_by(|a, b| key_order(a, b));
        assert_eq!(k, vec!["_x", "A", "a2", "a10", "b"]);
    }
}
