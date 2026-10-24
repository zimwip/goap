//! A YAML document read as Go's `gopkg.in/yaml.v3` reads it: the node tree its parser composes from libyaml's events
//! (`parser.parse`: tags shortened, scalars resolved, anchors kept by name, lines from 1), the tag and value a scalar
//! resolves to (`resolve`), and the words of its failures (`yaml: line N: …`).
use libyaml::{ErrorKind, EventKind, Parser, ScalarStyle};

pub const NULL_TAG: &str = "!!null";
pub const BOOL_TAG: &str = "!!bool";
pub const STR_TAG: &str = "!!str";
pub const INT_TAG: &str = "!!int";
pub const FLOAT_TAG: &str = "!!float";
pub const TIMESTAMP_TAG: &str = "!!timestamp";
pub const SEQ_TAG: &str = "!!seq";
pub const MAP_TAG: &str = "!!map";
pub const BINARY_TAG: &str = "!!binary";
pub const MERGE_TAG: &str = "!!merge";

const LONG_TAG_PREFIX: &str = "tag:yaml.org,2002:";

/// A failure that stops the read (Go's `failf`, `yaml: …`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Fatal(pub String);

impl Fatal {
    pub fn new(msg: impl std::fmt::Display) -> Fatal {
        Fatal(format!("yaml: {msg}"))
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    Document,
    Sequence,
    Mapping,
    Scalar,
    Alias,
}

/// The style flags of a node (yaml.v3's `Style`).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Style {
    pub tagged: bool,
    pub double_quoted: bool,
    pub single_quoted: bool,
    pub literal: bool,
    pub folded: bool,
    pub flow: bool,
}

/// The index of a node in its [`Tree`].
pub type Id = usize;

#[derive(Debug, Clone)]
pub struct Node {
    pub kind: Kind,
    pub tag: String,
    pub value: String,
    pub style: Style,
    pub line: u64,
    pub column: u64,
    pub content: Vec<Id>,
    /// the node an alias stands for
    pub alias: Option<Id>,
}

/// The nodes of a document, the root first.
#[derive(Debug, Clone, Default)]
pub struct Tree {
    pub nodes: Vec<Node>,
}

impl Tree {
    pub fn node(&self, id: Id) -> &Node {
        &self.nodes[id]
    }

    /// Whether a scalar is a string by its style or tag (`indicatedString`).
    pub fn indicated_string(&self, id: Id) -> bool {
        let n = self.node(id);
        n.kind == Kind::Scalar
            && (short_tag(&n.tag) == STR_TAG
                || (n.tag.is_empty() || n.tag == "!")
                    && (n.style.single_quoted || n.style.double_quoted || n.style.literal || n.style.folded))
    }

    /// The tag of a node, resolved (`Node.ShortTag`).
    pub fn short_tag(&self, id: Id) -> String {
        if self.indicated_string(id) {
            return STR_TAG.into();
        }
        let n = self.node(id);
        if n.tag.is_empty() || n.tag == "!" {
            return match n.kind {
                Kind::Mapping => MAP_TAG.into(),
                Kind::Sequence => SEQ_TAG.into(),
                Kind::Alias => n.alias.map(|a| self.short_tag(a)).unwrap_or_default(),
                Kind::Scalar => resolve("", &n.value).map(|(t, _)| t).unwrap_or_default(),
                Kind::Document => String::new(),
            };
        }
        short_tag(&n.tag)
    }
}

/// The short form of a tag (`!!str` for `tag:yaml.org,2002:str`).
pub fn short_tag(tag: &str) -> String {
    match tag.strip_prefix(LONG_TAG_PREFIX) {
        Some(rest) => format!("!!{rest}"),
        None => tag.to_string(),
    }
}

/// The first document of a stream, as `Decoder.Decode` parses it: `None` for a stream holding no document (Go's
/// `io.EOF`).
pub fn parse_first(src: &[u8]) -> Result<Option<Tree>, Fatal> {
    let mut p = Composer {
        parser: Parser::new(src),
        peeked: None,
        tree: Tree::default(),
        anchors: Default::default(),
    };
    p.expect_stream_start()?;
    if matches!(p.peek()?.kind, EventKind::StreamEnd) {
        return Ok(None);
    }
    p.parse()?;
    // yaml.v3's scanner reads two tokens ahead of its parser (for the comments): a token past the document that does
    // not scan fails the read there too, where libyaml would stop at the document's end.
    if let Err(e) = p.parser.next_event()
        && matches!(e.kind, ErrorKind::Scanner | ErrorKind::Reader)
    {
        return Err(Composer::fail(e));
    }
    Ok(Some(p.tree))
}

struct Composer {
    parser: Parser,
    peeked: Option<libyaml::Event>,
    tree: Tree,
    anchors: std::collections::HashMap<String, Id>,
}

impl Composer {
    fn fail(e: libyaml::Error) -> Fatal {
        let scanner = u64::from(e.kind == ErrorKind::Scanner);
        let line = if e.context_mark.line != 0 {
            e.context_mark.line + scanner
        } else if e.problem_mark.line != 0 {
            e.problem_mark.line + scanner
        } else {
            0
        };
        let at = if line != 0 {
            format!("line {line}: ")
        } else {
            String::new()
        };
        let msg = if e.problem.is_empty() {
            "unknown problem parsing YAML content".to_string()
        } else {
            e.problem
        };
        Fatal::new(format!("{at}{msg}"))
    }

    fn peek(&mut self) -> Result<&libyaml::Event, Fatal> {
        if self.peeked.is_none() {
            self.peeked = Some(self.parser.next_event().map_err(Self::fail)?);
        }
        Ok(self.peeked.as_ref().expect("an event just read"))
    }

    fn take(&mut self) -> Result<libyaml::Event, Fatal> {
        self.peek()?;
        Ok(self.peeked.take().expect("an event just read"))
    }

    fn expect_stream_start(&mut self) -> Result<(), Fatal> {
        self.take().map(drop)
    }

    fn node(&mut self, kind: Kind, default_tag: &str, tag: &str, value: String, ev: &libyaml::Event) -> Id {
        let mut style = Style::default();
        let tag = if !tag.is_empty() && tag != "!" {
            style.tagged = true;
            short_tag(tag)
        } else if !default_tag.is_empty() {
            default_tag.to_string()
        } else if kind == Kind::Scalar {
            resolve("", &value).map(|(t, _)| t).unwrap_or_default()
        } else {
            tag.to_string()
        };
        self.tree.nodes.push(Node {
            kind,
            tag,
            value,
            style,
            line: ev.start.line + 1,
            column: ev.start.column + 1,
            content: Vec::new(),
            alias: None,
        });
        self.tree.nodes.len() - 1
    }

    fn anchor(&mut self, id: Id, anchor: Option<String>) {
        if let Some(a) = anchor {
            self.anchors.insert(a, id);
        }
    }

    fn parse(&mut self) -> Result<Id, Fatal> {
        let ev = self.take()?;
        match ev.kind.clone() {
            EventKind::Scalar {
                anchor,
                tag,
                value,
                style,
            } => {
                let default = match style {
                    ScalarStyle::Plain if value == "<<" => MERGE_TAG,
                    ScalarStyle::Plain => "",
                    _ => STR_TAG,
                };
                let id = self.node(Kind::Scalar, default, tag.as_deref().unwrap_or_default(), value, &ev);
                let s = &mut self.tree.nodes[id].style;
                match style {
                    ScalarStyle::DoubleQuoted => s.double_quoted = true,
                    ScalarStyle::SingleQuoted => s.single_quoted = true,
                    ScalarStyle::Literal => s.literal = true,
                    ScalarStyle::Folded => s.folded = true,
                    ScalarStyle::Plain => {}
                }
                self.anchor(id, anchor);
                Ok(id)
            }
            EventKind::Alias { anchor } => {
                let id = self.node(Kind::Alias, "", "", anchor.clone(), &ev);
                let Some(&target) = self.anchors.get(&anchor) else {
                    return Err(Fatal::new(format!("unknown anchor '{anchor}' referenced")));
                };
                self.tree.nodes[id].alias = Some(target);
                Ok(id)
            }
            EventKind::SequenceStart { anchor, tag, flow } => {
                let id = self.node(
                    Kind::Sequence,
                    SEQ_TAG,
                    tag.as_deref().unwrap_or_default(),
                    String::new(),
                    &ev,
                );
                self.tree.nodes[id].style.flow = flow;
                self.anchor(id, anchor);
                while !matches!(self.peek()?.kind, EventKind::SequenceEnd) {
                    let child = self.parse()?;
                    self.tree.nodes[id].content.push(child);
                }
                self.take()?;
                Ok(id)
            }
            EventKind::MappingStart { anchor, tag, flow } => {
                let id = self.node(
                    Kind::Mapping,
                    MAP_TAG,
                    tag.as_deref().unwrap_or_default(),
                    String::new(),
                    &ev,
                );
                self.tree.nodes[id].style.flow = flow;
                self.anchor(id, anchor);
                while !matches!(self.peek()?.kind, EventKind::MappingEnd) {
                    let k = self.parse()?;
                    let v = self.parse()?;
                    self.tree.nodes[id].content.extend([k, v]);
                }
                self.take()?;
                Ok(id)
            }
            EventKind::DocumentStart => {
                let id = self.node(Kind::Document, "", "", String::new(), &ev);
                let child = self.parse()?;
                self.tree.nodes[id].content.push(child);
                match self.take()?.kind {
                    EventKind::DocumentEnd => Ok(id),
                    other => Err(Fatal::new(format!("expected DOCUMENT_END event but got {other:?}"))),
                }
            }
            EventKind::StreamEnd => Err(Fatal::new("attempted to go past the end of stream; corrupted value?")),
            other => Err(Fatal::new(format!("unexpected event {other:?}"))),
        }
    }
}

/// A time as yaml.v3 reads a timestamp: its fields, the offset of its zone in seconds (`None`: UTC).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Timestamp {
    pub year: i32,
    pub month: u32,
    pub day: u32,
    pub hour: u32,
    pub minute: u32,
    pub second: u32,
    pub nanos: u32,
    pub offset: Option<i32>,
}

impl Timestamp {
    /// The text `time.Time.MarshalJSON` writes (RFC 3339 with nanoseconds, the trailing zeros of the fraction left
    /// out), unquoted.
    pub fn rfc3339_nano(&self) -> String {
        let mut out = format!(
            "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
            self.year, self.month, self.day, self.hour, self.minute, self.second
        );
        if self.nanos != 0 {
            let f = format!("{:09}", self.nanos);
            out.push('.');
            out.push_str(f.trim_end_matches('0'));
        }
        match self.offset {
            None | Some(0) => out.push('Z'),
            Some(o) => {
                let (sign, o) = if o < 0 { ('-', -o) } else { ('+', o) };
                out.push_str(&format!("{sign}{:02}:{:02}", o / 3600, o / 60 % 60));
            }
        }
        out
    }
}

/// The value a scalar resolves to (what yaml.v3 decodes into an `interface{}`).
#[derive(Debug, Clone, PartialEq)]
pub enum Resolved {
    Null,
    Bool(bool),
    Int(i64),
    Uint(u64),
    Float(f64),
    Str(String),
    Time(Timestamp),
}

fn resolvable(tag: &str) -> bool {
    matches!(
        tag,
        "" | STR_TAG | BOOL_TAG | INT_TAG | FLOAT_TAG | NULL_TAG | TIMESTAMP_TAG
    )
}

/// The tag and value of a scalar of a tag (`resolve`); a value that cannot have the tag it names fails.
pub fn resolve(tag: &str, s: &str) -> Result<(String, Resolved), Fatal> {
    let tag = short_tag(tag);
    if !resolvable(&tag) {
        return Ok((tag, Resolved::Str(s.to_string())));
    }
    let (rtag, out) = resolved(&tag, s);
    match tag.as_str() {
        "" | STR_TAG | BINARY_TAG => Ok((rtag, out)),
        t if t == rtag => Ok((rtag, out)),
        FLOAT_TAG if rtag == INT_TAG => match out {
            Resolved::Int(i) => Ok((FLOAT_TAG.into(), Resolved::Float(i as f64))),
            _ => Err(Fatal::new(format!("cannot decode {rtag} `{s}` as a {tag}"))),
        },
        _ => Err(Fatal::new(format!("cannot decode {rtag} `{s}` as a {tag}"))),
    }
}

fn resolved(tag: &str, s: &str) -> (String, Resolved) {
    let t = |tag: &str, v: Resolved| (tag.to_string(), v);
    let hint = match s.as_bytes().first() {
        None => b'N',
        Some(b'+' | b'-') => b'S',
        Some(b'0'..=b'9') => b'D',
        Some(b'y' | b'Y' | b'n' | b'N' | b't' | b'T' | b'f' | b'F' | b'o' | b'O' | b'~') => b'M',
        Some(b'.') => b'.',
        Some(_) => 0,
    };
    if hint == 0 || tag == STR_TAG || tag == BINARY_TAG {
        return t(STR_TAG, Resolved::Str(s.into()));
    }
    match s {
        "true" | "True" | "TRUE" => return t(BOOL_TAG, Resolved::Bool(true)),
        "false" | "False" | "FALSE" => return t(BOOL_TAG, Resolved::Bool(false)),
        "" | "~" | "null" | "Null" | "NULL" => return t(NULL_TAG, Resolved::Null),
        ".nan" | ".NaN" | ".NAN" => return t(FLOAT_TAG, Resolved::Float(f64::NAN)),
        ".inf" | ".Inf" | ".INF" | "+.inf" | "+.Inf" | "+.INF" => {
            return t(FLOAT_TAG, Resolved::Float(f64::INFINITY));
        }
        "-.inf" | "-.Inf" | "-.INF" => return t(FLOAT_TAG, Resolved::Float(f64::NEG_INFINITY)),
        "<<" => return t(MERGE_TAG, Resolved::Str("<<".into())),
        _ => {}
    }
    match hint {
        b'.' => {
            if let Some(f) = parse_float(s) {
                return t(FLOAT_TAG, Resolved::Float(f));
            }
        }
        b'D' | b'S' => {
            if (tag.is_empty() || tag == TIMESTAMP_TAG)
                && let Some(ts) = parse_timestamp(s)
            {
                return t(TIMESTAMP_TAG, Resolved::Time(ts));
            }
            let plain = s.replace('_', "");
            if let Some(i) = parse_int(&plain, 0) {
                return t(INT_TAG, Resolved::Int(i));
            }
            if let Some(u) = parse_uint(&plain, 0) {
                return t(INT_TAG, Resolved::Uint(u));
            }
            if yaml_float(&plain)
                && let Some(f) = parse_float(&plain)
            {
                return t(FLOAT_TAG, Resolved::Float(f));
            }
            for (prefix, radix, neg) in [("0b", 2, false), ("-0b", 2, true), ("0o", 8, false), ("-0o", 8, true)] {
                let Some(rest) = plain.strip_prefix(prefix) else {
                    continue;
                };
                let digits = if neg { format!("-{rest}") } else { rest.to_string() };
                if let Some(i) = parse_int(&digits, radix) {
                    return t(INT_TAG, Resolved::Int(i));
                }
                if !neg && let Some(u) = parse_uint(rest, radix) {
                    return t(INT_TAG, Resolved::Uint(u));
                }
                break;
            }
        }
        _ => {}
    }
    t(STR_TAG, Resolved::Str(s.into()))
}

/// Go's `strconv.ParseUint(s, base, 64)`, base 0 reading the prefix (`0x`, `0o`, `0b`, a leading `0` for octal).
pub fn parse_uint(s: &str, base: u32) -> Option<u64> {
    let (radix, digits) = if base == 0 {
        let lower = s.to_ascii_lowercase();
        if let Some(d) = lower.strip_prefix("0x") {
            (16, d.to_string())
        } else if let Some(d) = lower.strip_prefix("0o") {
            (8, d.to_string())
        } else if let Some(d) = lower.strip_prefix("0b") {
            (2, d.to_string())
        } else if lower.len() > 1 && lower.starts_with('0') {
            (8, lower[1..].to_string())
        } else {
            (10, lower)
        }
    } else {
        (base, s.to_string())
    };
    if digits.is_empty() || !digits.chars().all(|c| c.is_digit(radix)) {
        return None;
    }
    u64::from_str_radix(&digits, radix).ok()
}

/// Go's `strconv.ParseInt(s, base, 64)`.
pub fn parse_int(s: &str, base: u32) -> Option<i64> {
    let (neg, body) = match s.as_bytes().first() {
        Some(b'+') => (false, &s[1..]),
        Some(b'-') => (true, &s[1..]),
        _ => (false, s),
    };
    let u = parse_uint(body, base)?;
    if neg {
        (u <= 1u64 << 63).then(|| (u as i64).wrapping_neg())
    } else {
        i64::try_from(u).ok()
    }
}

/// Go's `strconv.ParseFloat(s, 64)` of a decimal text: none on an overflow.
fn parse_float(s: &str) -> Option<f64> {
    if s.contains('_') || s.chars().any(|c| c.is_ascii_alphabetic() && !matches!(c, 'e' | 'E')) {
        return None;
    }
    s.parse::<f64>().ok().filter(|f| f.is_finite())
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

/// A timestamp in one of the forms yaml.v3 reads (`parseTimestamp`: `2006-1-2T15:4:5.999999999Z07:00`, the same with
/// a lower-case `t`, `2006-1-2 15:4:5.999999999`, `2006-1-2`).
pub fn parse_timestamp(s: &str) -> Option<Timestamp> {
    let b = s.as_bytes();
    if b.len() < 5 || !b[..4].iter().all(u8::is_ascii_digit) || b[4] != b'-' {
        return None;
    }
    let mut p = Cursor { b, i: 5 };
    let year: i32 = s[..4].parse().ok()?;
    let month = p.num()?;
    p.eat(b'-').then_some(())?;
    let day = p.num()?;
    if !(1..=12).contains(&month) || day < 1 || day > days_in(year, month) {
        return None;
    }
    let mut ts = Timestamp {
        year,
        month,
        day,
        hour: 0,
        minute: 0,
        second: 0,
        nanos: 0,
        offset: None,
    };
    if p.done() {
        return Some(ts);
    }
    let sep = b[p.i];
    if !matches!(sep, b'T' | b't' | b' ') {
        return None;
    }
    p.i += 1;
    ts.hour = p.num()?;
    p.eat(b':').then_some(())?;
    ts.minute = p.num()?;
    p.eat(b':').then_some(())?;
    ts.second = p.num()?;
    if ts.hour > 23 || ts.minute > 59 || ts.second > 59 {
        return None;
    }
    if p.eat(b'.') {
        let from = p.i;
        if p.digits() == 0 {
            return None;
        }
        let frac = &s[from..p.i];
        let nine: String = frac.chars().chain(std::iter::repeat('0')).take(9).collect();
        ts.nanos = nine.parse().ok()?;
    }
    if sep == b' ' {
        return p.done().then_some(ts);
    }
    if p.eat(b'Z') {
        return p.done().then_some(Timestamp { offset: Some(0), ..ts });
    }
    let neg = if p.eat(b'+') {
        false
    } else if p.eat(b'-') {
        true
    } else {
        return None;
    };
    let zh = p.fixed2()?;
    p.eat(b':').then_some(())?;
    let zm = p.fixed2()?;
    if zh > 24 || zm > 59 || !p.done() {
        return None;
    }
    let o = (zh * 3600 + zm * 60) as i32;
    ts.offset = Some(if neg { -o } else { o });
    Some(ts)
}

struct Cursor<'a> {
    b: &'a [u8],
    i: usize,
}

impl Cursor<'_> {
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

fn days_in(year: i32, month: u32) -> u32 {
    match month {
        2 if year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolves_as_yaml_v3() {
        let r = |s: &str| resolve("", s).unwrap();
        assert_eq!(r("1.10"), (FLOAT_TAG.into(), Resolved::Float(1.1)));
        assert_eq!(r("0x1F"), (INT_TAG.into(), Resolved::Int(31)));
        assert_eq!(r("010"), (INT_TAG.into(), Resolved::Int(8)));
        assert_eq!(r("1e400"), (STR_TAG.into(), Resolved::Str("1e400".into())));
        assert_eq!(r("yes"), (STR_TAG.into(), Resolved::Str("yes".into())));
        assert_eq!(r("~"), (NULL_TAG.into(), Resolved::Null));
        assert_eq!(r("18446744073709551615").1, Resolved::Uint(u64::MAX));
        let (tag, v) = r("2020-01-02");
        assert_eq!(tag, TIMESTAMP_TAG);
        let Resolved::Time(ts) = v else { panic!() };
        assert_eq!(ts.rfc3339_nano(), "2020-01-02T00:00:00Z");
        assert_eq!(
            resolve("!!int", "x").unwrap_err().0,
            "yaml: cannot decode !!str `x` as a !!int"
        );
        assert_eq!(resolve("!!float", "3").unwrap().1, Resolved::Float(3.0));
    }

    #[test]
    fn composes_the_first_document() {
        let t = parse_first(b"a: &x 1\nb: *x\n---\nc: 2\n").unwrap().unwrap();
        let doc = t.node(0);
        assert_eq!(doc.kind, Kind::Document);
        let m = t.node(doc.content[0]);
        assert_eq!(m.content.len(), 4);
        let alias = t.node(m.content[3]);
        assert_eq!(alias.kind, Kind::Alias);
        assert_eq!(t.short_tag(m.content[3]), INT_TAG);
        assert!(parse_first(b"# nothing\n").unwrap().is_none());
        assert_eq!(
            parse_first(b"a: b\n c: d\n").unwrap_err().0,
            "yaml: line 2: mapping values are not allowed in this context"
        );
        assert_eq!(
            parse_first(b"a: *y\n").unwrap_err().0,
            "yaml: unknown anchor 'y' referenced"
        );
    }
}
