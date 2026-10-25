//! JSON read as Go's `encoding/json` `Decoder` reads it into a struct with `DisallowUnknownFields`, its errors in its
//! words: the scanner's syntax errors over the first value of the text (`invalid character 'o' in literal null
//! (expecting 'u')`, `unexpected EOF`), then the decoding into the struct's types, the first type error or unknown field
//! kept while the decoding goes on, an error of a `time.Time` stopping it (`Time.UnmarshalJSON`, `time.Parse`'s
//! words). What reads gives the value as the Rust structs read it: the fields by their JSON names (a key matched
//! without regard to case renamed), a null leaving a field as it was, as Go's does.
use chrono::{DateTime, FixedOffset, NaiveDate};
use serde_json::{Map, Value};

/// A Go type of the shape decoded.
pub enum Ty {
    Str,
    Bool,
    Int,
    /// `interface{}`
    Any,
    /// `time.Time`, decoded by its `UnmarshalJSON`
    Time,
    /// a slice: its element, its Go name
    Slice(&'static Ty, &'static str),
    /// `map[string]interface{}`
    Map,
    /// a pointer to a struct
    Ptr(&'static Struct),
    Struct(&'static Struct),
}

/// A Go struct: its name, its qualified name, its fields by their JSON name.
pub struct Struct {
    pub name: &'static str,
    pub go: &'static str,
    pub fields: &'static [(&'static str, Ty)],
}

/// Why a text does not decode.
#[derive(Debug, PartialEq)]
pub enum Fail {
    /// Go's error
    Go(String),
    /// a text Go reads that is not read here (nested past [`MAX_DEPTH`], a time the Rust types do not hold)
    Unsure,
}

/// How deep a value is read here; Go reads up to 10000.
pub const MAX_DEPTH: usize = 128;

/// Decodes the first JSON value of a text into a struct, as `json.NewDecoder(…).Decode` does with
/// `DisallowUnknownFields`: the value with the struct's JSON names, or why it does not decode.
pub fn decode(data: &[u8], root: &'static Struct) -> Result<Value, Fail> {
    let end = first_value(data).map_err(Fail::Go)?;
    let tree = Reader { b: &data[..end], i: 0 }.value(0).ok_or(Fail::Unsure)?;
    let mut d = Decoder {
        saved: None,
        within: None,
        fields: Vec::new(),
    };
    let out = d.value(&Ty::Struct(root), &tree)?;
    if let Some(e) = d.saved {
        return Err(Fail::Go(e));
    }
    Ok(match out {
        Out::Set(v) => v,
        _ => Value::Object(Map::new()),
    })
}

/// The scanner's states (`encoding/json/scanner.go`).
#[derive(Clone, Copy, PartialEq)]
enum State {
    BeginValue,
    BeginValueOrEmpty,
    BeginStringOrEmpty,
    BeginString,
    EndValue,
    InString,
    InStringEsc,
    EscU(u8),
    Neg,
    One,
    Zero,
    Dot,
    Dot0,
    E,
    ESign,
    E0,
    /// a literal: its letters, how many are read, its name
    Lit(&'static [u8], usize, &'static str),
}

#[derive(Clone, Copy)]
enum Parse {
    ObjectKey,
    ObjectValue,
    ArrayValue,
}

fn space(c: u8) -> bool {
    matches!(c, b' ' | b'\t' | b'\r' | b'\n')
}

/// A byte in an error, as the scanner's `quoteChar` writes it.
fn quote_char(c: u8) -> String {
    match c {
        b'\'' => "'\\''".into(),
        b'"' => "'\"'".into(),
        _ => {
            let q = go_quote(&char::from(c).to_string());
            format!("'{}'", &q[1..q.len() - 1])
        }
    }
}

/// A string as `strconv.Quote` writes it.
pub fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            c if printable(c) => out.push(c),
            c if (c as u32) < 0x80 => out.push_str(&format!("\\x{:02x}", c as u32)),
            c if (c as u32) < 0x10000 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push_str(&format!("\\U{:08x}", c as u32)),
        }
    }
    out.push('"');
    out
}

/// Go's `unicode.IsPrint` for what a JSON text holds: the ASCII space, no control, no other space, no format
/// character.
fn printable(c: char) -> bool {
    if c == ' ' {
        return true;
    }
    if c.is_control() || c.is_whitespace() {
        return false;
    }
    let u = c as u32;
    // format characters (Cf) and the unassigned or private planes Go does not print
    !matches!(u, 0xad | 0x600..=0x605 | 0x61c | 0x6dd | 0x70f | 0x180e | 0x200b..=0x200f | 0x202a..=0x202e
        | 0x2060..=0x2064 | 0x2066..=0x206f | 0xfeff | 0xfff9..=0xfffb | 0xd800..=0xf8ff | 0xe0000..)
}

/// Whether a text is one JSON value, spaces around it, as Go's `json.Valid` tells (its strings may hold bytes that
/// are no UTF-8).
pub fn valid(b: &[u8]) -> bool {
    first_value(b).is_ok_and(|n| b[n..].iter().all(|&c| space(c)))
}

/// Where the first value of a text ends, as `Decoder.readValue` scans it; its error otherwise.
fn first_value(b: &[u8]) -> Result<usize, String> {
    let mut st = State::BeginValue;
    let mut stack: Vec<Parse> = Vec::new();
    let err = |c: u8, ctx: &str| format!("invalid character {} {ctx}", quote_char(c));
    for (i, &c) in b.iter().enumerate() {
        let mut c_state = st;
        // a state ending a value hands the byte on to `stateEndValue`, as Go's step functions do
        loop {
            match c_state {
                State::BeginValueOrEmpty if c == b']' => c_state = State::EndValue,
                State::BeginValueOrEmpty if space(c) => break,
                State::BeginValueOrEmpty => c_state = State::BeginValue,
                State::BeginStringOrEmpty if c == b'}' => {
                    if let Some(top) = stack.last_mut() {
                        *top = Parse::ObjectValue;
                    }
                    c_state = State::EndValue;
                }
                State::BeginStringOrEmpty if space(c) => break,
                State::BeginStringOrEmpty => c_state = State::BeginString,
                State::BeginValue => {
                    if space(c) {
                        break;
                    }
                    st = match c {
                        b'{' | b'[' => {
                            stack.push(if c == b'{' { Parse::ObjectKey } else { Parse::ArrayValue });
                            if stack.len() > 10000 {
                                return Err(err(c, "exceeded max depth"));
                            }
                            if c == b'{' {
                                State::BeginStringOrEmpty
                            } else {
                                State::BeginValueOrEmpty
                            }
                        }
                        b'"' => State::InString,
                        b'-' => State::Neg,
                        b'0' => State::Zero,
                        b'1'..=b'9' => State::One,
                        b't' => State::Lit(b"rue", 0, "true"),
                        b'f' => State::Lit(b"alse", 0, "false"),
                        b'n' => State::Lit(b"ull", 0, "null"),
                        _ => return Err(err(c, "looking for beginning of value")),
                    };
                    break;
                }
                State::BeginString => {
                    if space(c) {
                        break;
                    }
                    if c != b'"' {
                        return Err(err(c, "looking for beginning of object key string"));
                    }
                    st = State::InString;
                    break;
                }
                State::EndValue => {
                    let Some(top) = stack.last_mut() else {
                        return Ok(i); // the byte after a value at the top
                    };
                    if space(c) {
                        st = State::EndValue;
                        break;
                    }
                    match (*top, c) {
                        (Parse::ObjectKey, b':') => {
                            *top = Parse::ObjectValue;
                            st = State::BeginValue;
                        }
                        (Parse::ObjectKey, _) => return Err(err(c, "after object key")),
                        (Parse::ObjectValue, b',') => {
                            *top = Parse::ObjectKey;
                            st = State::BeginString;
                        }
                        (Parse::ObjectValue, b'}') | (Parse::ArrayValue, b']') => {
                            stack.pop();
                            if stack.is_empty() {
                                return Ok(i + 1);
                            }
                            st = State::EndValue;
                        }
                        (Parse::ObjectValue, _) => return Err(err(c, "after object key:value pair")),
                        (Parse::ArrayValue, b',') => st = State::BeginValue,
                        (Parse::ArrayValue, _) => return Err(err(c, "after array element")),
                    }
                    break;
                }
                State::InString => {
                    st = match c {
                        b'"' => State::EndValue,
                        b'\\' => State::InStringEsc,
                        c if c < 0x20 => return Err(err(c, "in string literal")),
                        _ => State::InString,
                    };
                    break;
                }
                State::InStringEsc => {
                    st = match c {
                        b'b' | b'f' | b'n' | b'r' | b't' | b'\\' | b'/' | b'"' => State::InString,
                        b'u' => State::EscU(0),
                        _ => return Err(err(c, "in string escape code")),
                    };
                    break;
                }
                State::EscU(n) => {
                    if !c.is_ascii_hexdigit() {
                        return Err(err(c, "in \\u hexadecimal character escape"));
                    }
                    st = if n == 3 { State::InString } else { State::EscU(n + 1) };
                    break;
                }
                State::Neg => {
                    st = match c {
                        b'0' => State::Zero,
                        b'1'..=b'9' => State::One,
                        _ => return Err(err(c, "in numeric literal")),
                    };
                    break;
                }
                State::One if c.is_ascii_digit() => break,
                State::One => c_state = State::Zero,
                State::Zero => match c {
                    b'.' => {
                        st = State::Dot;
                        break;
                    }
                    b'e' | b'E' => {
                        st = State::E;
                        break;
                    }
                    _ => c_state = State::EndValue,
                },
                State::Dot => {
                    if !c.is_ascii_digit() {
                        return Err(err(c, "after decimal point in numeric literal"));
                    }
                    st = State::Dot0;
                    break;
                }
                State::Dot0 => match c {
                    c if c.is_ascii_digit() => break,
                    b'e' | b'E' => {
                        st = State::E;
                        break;
                    }
                    _ => c_state = State::EndValue,
                },
                State::E if c == b'+' || c == b'-' => {
                    st = State::ESign;
                    break;
                }
                State::E | State::ESign => {
                    if !c.is_ascii_digit() {
                        return Err(err(c, "in exponent of numeric literal"));
                    }
                    st = State::E0;
                    break;
                }
                State::E0 if c.is_ascii_digit() => break,
                State::E0 => c_state = State::EndValue,
                State::Lit(rest, n, name) => {
                    if c != rest[n] {
                        let want = rest[n] as char;
                        return Err(err(c, &format!("in literal {name} (expecting '{want}')")));
                    }
                    st = if n + 1 == rest.len() {
                        State::EndValue
                    } else {
                        State::Lit(rest, n + 1, name)
                    };
                    break;
                }
            }
        }
    }
    // the end of the text: a value ends there when a space would end it
    let ends = stack.is_empty() && matches!(st, State::EndValue | State::One | State::Zero | State::Dot0 | State::E0);
    if ends {
        return Ok(b.len());
    }
    Err(if b.iter().any(|c| !space(*c)) {
        "unexpected EOF".into()
    } else {
        "EOF".into()
    })
}

/// A JSON value its keys in their order, its strings as Go unquotes them, the text of each literal kept.
enum Json {
    Null,
    Bool(bool),
    Num(String),
    /// the string and its text between its quotes, escapes as written
    Str(String, String),
    Arr(Vec<Json>),
    Obj(Vec<(String, Json)>),
}

/// Reads a value the scanner checked.
struct Reader<'b> {
    b: &'b [u8],
    i: usize,
}

impl Reader<'_> {
    fn skip(&mut self) {
        while self.i < self.b.len() && space(self.b[self.i]) {
            self.i += 1;
        }
    }

    fn value(&mut self, depth: usize) -> Option<Json> {
        if depth > MAX_DEPTH {
            return None;
        }
        self.skip();
        let c = *self.b.get(self.i)?;
        Some(match c {
            b'{' => {
                self.i += 1;
                let mut out = Vec::new();
                loop {
                    self.skip();
                    if self.b[self.i] == b'}' {
                        self.i += 1;
                        break;
                    }
                    if self.b[self.i] == b',' {
                        self.i += 1;
                        self.skip();
                    }
                    let Json::Str(k, _) = self.string()? else { return None };
                    self.skip();
                    self.i += 1; // ':'
                    out.push((k, self.value(depth + 1)?));
                }
                Json::Obj(out)
            }
            b'[' => {
                self.i += 1;
                let mut out = Vec::new();
                loop {
                    self.skip();
                    if self.b[self.i] == b']' {
                        self.i += 1;
                        break;
                    }
                    if self.b[self.i] == b',' {
                        self.i += 1;
                    }
                    out.push(self.value(depth + 1)?);
                }
                Json::Arr(out)
            }
            b'"' => self.string()?,
            b't' | b'f' | b'n' => {
                let (v, n) = match c {
                    b't' => (Json::Bool(true), 4),
                    b'f' => (Json::Bool(false), 5),
                    _ => (Json::Null, 4),
                };
                self.i += n;
                v
            }
            _ => {
                let start = self.i;
                while self.i < self.b.len() && matches!(self.b[self.i], b'0'..=b'9' | b'-' | b'+' | b'.' | b'e' | b'E')
                {
                    self.i += 1;
                }
                Json::Num(String::from_utf8_lossy(&self.b[start..self.i]).into_owned())
            }
        })
    }

    /// A string, unquoted as Go's `unquote` does: a surrogate not paired is U+FFFD.
    fn string(&mut self) -> Option<Json> {
        self.i += 1;
        let start = self.i;
        let mut out = Vec::new();
        while self.b[self.i] != b'"' {
            if self.b[self.i] != b'\\' {
                out.push(self.b[self.i]);
                self.i += 1;
                continue;
            }
            let e = self.b[self.i + 1];
            self.i += 2;
            let c = match e {
                b'b' => '\u{8}',
                b'f' => '\u{c}',
                b'n' => '\n',
                b'r' => '\r',
                b't' => '\t',
                b'u' => {
                    let r = self.u4(self.i)?;
                    self.i += 4;
                    let mut c = char::from_u32(r).unwrap_or('\u{fffd}');
                    if (0xd800..0xe000).contains(&r) {
                        c = '\u{fffd}';
                        let low = (self.b.get(self.i) == Some(&b'\\') && self.b.get(self.i + 1) == Some(&b'u'))
                            .then(|| self.u4(self.i + 2))
                            .flatten();
                        if let Some(low) = low.filter(|l| r < 0xdc00 && (0xdc00..0xe000).contains(l)) {
                            c = char::from_u32(0x10000 + ((r - 0xd800) << 10) + (low - 0xdc00))?;
                            self.i += 6;
                        }
                    }
                    c
                }
                e => e as char,
            };
            let mut buf = [0; 4];
            out.extend_from_slice(c.encode_utf8(&mut buf).as_bytes());
        }
        let raw = String::from_utf8_lossy(&self.b[start..self.i]).into_owned();
        self.i += 1;
        Some(Json::Str(String::from_utf8_lossy(&out).into_owned(), raw))
    }

    fn u4(&self, at: usize) -> Option<u32> {
        let h = self.b.get(at..at + 4)?;
        u32::from_str_radix(std::str::from_utf8(h).ok()?, 16).ok()
    }
}

/// What a value does to the field it is decoded into.
enum Out {
    /// leaves it as it was (a null into a string, a value refused)
    Keep,
    /// sets it to nil
    Clear,
    Set(Value),
}

struct Decoder {
    /// the first error kept, as Go's `saveError`
    saved: Option<String>,
    /// the struct and the JSON names of the fields being decoded, as Go's `errorContext`
    within: Option<&'static str>,
    fields: Vec<&'static str>,
}

impl Decoder {
    fn save(&mut self, e: String) {
        self.saved.get_or_insert(e);
    }

    /// `UnmarshalTypeError`, in the context of the field being decoded.
    fn mismatch(&mut self, what: &str, ty: &str) -> Out {
        let e = match self.within {
            Some(s) => format!(
                "json: cannot unmarshal {what} into Go struct field {s}.{} of type {ty}",
                self.fields.join(".")
            ),
            None => format!("json: cannot unmarshal {what} into Go value of type {ty}"),
        };
        self.save(e);
        Out::Keep
    }

    fn value(&mut self, t: &Ty, j: &Json) -> Result<Out, Fail> {
        let name = |t: &Ty| -> &'static str {
            match t {
                Ty::Str => "string",
                Ty::Bool => "bool",
                Ty::Int => "int",
                Ty::Slice(_, go) => go,
                Ty::Map => "map[string]interface {}",
                Ty::Ptr(s) | Ty::Struct(s) => s.go,
                Ty::Any | Ty::Time => "",
            }
        };
        if let Ty::Time = t {
            return match j {
                Json::Null => Ok(Out::Keep),
                Json::Str(_, raw) => match parse_time(raw.as_bytes()) {
                    Ok(Some(t)) => Ok(Out::Set(Value::String(t.to_rfc3339()))),
                    Ok(None) => Err(Fail::Unsure),
                    Err(e) => Err(Fail::Go(e)),
                },
                _ => Err(Fail::Go("Time.UnmarshalJSON: input is not a JSON string".into())),
            };
        }
        if let Ty::Any = t {
            return Ok(Out::Set(self.any(j)));
        }
        Ok(match (t, j) {
            (Ty::Slice(..) | Ty::Map | Ty::Ptr(_), Json::Null) => Out::Clear,
            (_, Json::Null) => Out::Keep,
            (Ty::Str, Json::Str(s, _)) => Out::Set(Value::String(s.clone())),
            (Ty::Bool, Json::Bool(b)) => Out::Set(Value::Bool(*b)),
            (Ty::Int, Json::Num(s)) => match s.parse::<i64>() {
                Ok(n) => Out::Set(Value::from(n)),
                Err(_) => self.mismatch(&format!("number {s}"), "int"),
            },
            (Ty::Slice(elem, _), Json::Arr(xs)) => {
                let mut out = Vec::with_capacity(xs.len());
                for x in xs {
                    out.push(match self.value(elem, x)? {
                        Out::Set(v) => v,
                        _ => zero(elem),
                    });
                }
                Out::Set(Value::Array(out))
            }
            (Ty::Map, Json::Obj(kv)) => {
                let mut out = Map::new();
                for (k, x) in kv {
                    out.insert(k.clone(), self.any(x));
                }
                Out::Set(Value::Object(out))
            }
            (Ty::Ptr(s) | Ty::Struct(s), Json::Obj(kv)) => Out::Set(self.strukt(s, kv)?),
            (t, j) => {
                let what = match j {
                    Json::Bool(_) => "bool",
                    Json::Num(_) => "number",
                    Json::Str(..) => "string",
                    Json::Arr(_) => "array",
                    _ => "object",
                };
                self.mismatch(what, name(t))
            }
        })
    }

    fn strukt(&mut self, s: &'static Struct, kv: &[(String, Json)]) -> Result<Value, Fail> {
        let mut out = Map::new();
        for (k, x) in kv {
            let field = s
                .fields
                .iter()
                .find(|(n, _)| n == k)
                .or_else(|| s.fields.iter().find(|(n, _)| fold(n) == fold(k)));
            let Some((name, t)) = field else {
                self.save(format!("json: unknown field {}", go_quote(k)));
                continue;
            };
            let (within, depth) = (self.within, self.fields.len());
            self.within = Some(s.name);
            self.fields.push(name);
            let v = self.value(t, x);
            self.within = within;
            self.fields.truncate(depth);
            match v? {
                Out::Keep => {}
                Out::Clear => {
                    out.remove(*name);
                }
                Out::Set(v) => {
                    out.insert(name.to_string(), v);
                }
            }
        }
        Ok(Value::Object(out))
    }

    /// A value into an `interface{}`: a number a float64, refused past its range.
    fn any(&mut self, j: &Json) -> Value {
        match j {
            Json::Null => Value::Null,
            Json::Bool(b) => Value::Bool(*b),
            Json::Num(s) => match s.parse::<f64>() {
                Ok(f) if f.is_finite() => serde_json::from_str(s).unwrap_or(Value::Null),
                _ => {
                    self.mismatch(&format!("number {s}"), "float64");
                    Value::Null
                }
            },
            Json::Str(s, _) => Value::String(s.clone()),
            Json::Arr(xs) => Value::Array(xs.iter().map(|x| self.any(x)).collect()),
            Json::Obj(kv) => {
                let mut out = Map::new();
                for (k, x) in kv {
                    let v = self.any(x);
                    out.insert(k.clone(), v);
                }
                Value::Object(out)
            }
        }
    }
}

/// The zero value of an element of a slice.
fn zero(t: &Ty) -> Value {
    match t {
        Ty::Struct(_) => Value::Object(Map::new()),
        _ => Value::Null,
    }
}

/// A key folded as Go's `foldName` folds it: ASCII letters in capitals, any other letter its simplest case.
fn fold(s: &str) -> String {
    s.chars()
        .map(|c| {
            if c.is_ascii() {
                return c.to_ascii_uppercase();
            }
            let mut up = c.to_uppercase();
            let f = match (up.next(), up.next()) {
                (Some(u), None) => u.to_lowercase().next().unwrap_or(u),
                _ => c,
            };
            // the Kelvin sign and the long s fold to an ASCII letter
            f.to_ascii_uppercase()
        })
        .collect()
}

/// A time as `Time.UnmarshalJSON` reads the text between its quotes (escapes as written): Go's `time.Parse` with
/// the layout RFC 3339 (strict checks disabled, `parseStrictRFC3339`): the time, None when the Rust types do not hold
/// it, or Go's error.
pub fn parse_time(value: &[u8]) -> Result<Option<DateTime<FixedOffset>>, String> {
    const LAYOUT: &str = "2006-01-02T15:04:05Z07:00";
    let cannot = |layout_elem: &str, value_elem: &[u8]| {
        format!(
            "parsing time {} as {}: cannot parse {} as {}",
            time_quote(value),
            time_quote(LAYOUT.as_bytes()),
            time_quote(value_elem),
            time_quote(layout_elem.as_bytes())
        )
    };
    let range = |what: &str| format!("parsing time {}: {what} out of range", time_quote(value));
    let digit = |v: &[u8], i: usize| v.get(i).is_some_and(u8::is_ascii_digit);
    // `getnum`: one or two digits, two when fixed
    let num = |v: &[u8], fixed: bool| -> Option<(u32, usize)> {
        if !digit(v, 0) {
            return None;
        }
        if !digit(v, 1) {
            return (!fixed).then(|| (u32::from(v[0] - b'0'), 1));
        }
        Some((u32::from(v[0] - b'0') * 10 + u32::from(v[1] - b'0'), 2))
    };
    let skip = |v: &mut &[u8], prefix: u8| -> Result<(), String> {
        if v.first() != Some(&prefix) {
            return Err(cannot(&(prefix as char).to_string(), v));
        }
        *v = &v[1..];
        Ok(())
    };
    let mut v = value;
    // 2006
    if v.len() < 4 || !digit(v, 0) || !v[..4].iter().all(u8::is_ascii_digit) {
        return Err(cannot("2006", v));
    }
    let year: i32 = std::str::from_utf8(&v[..4]).unwrap_or("0").parse().unwrap_or(0);
    v = &v[4..];
    skip(&mut v, b'-')?;
    // 01
    let Some((month, n)) = num(v, true) else {
        return Err(cannot("01", v));
    };
    v = &v[n..];
    if month == 0 || month > 12 {
        return Err(range("month"));
    }
    skip(&mut v, b'-')?;
    // 02
    let Some((day, n)) = num(v, true) else {
        return Err(cannot("02", v));
    };
    v = &v[n..];
    skip(&mut v, b'T')?;
    // 15
    let Some((hour, n)) = num(v, false) else {
        return Err(cannot("15", v));
    };
    v = &v[n..];
    if hour >= 24 {
        return Err(range("hour"));
    }
    skip(&mut v, b':')?;
    // 04
    let Some((min, n)) = num(v, true) else {
        return Err(cannot("04", v));
    };
    v = &v[n..];
    if min >= 60 {
        return Err(range("minute"));
    }
    skip(&mut v, b':')?;
    // 05, and a fractional second the layout does not name
    let Some((sec, n)) = num(v, true) else {
        return Err(cannot("05", v));
    };
    v = &v[n..];
    if sec >= 60 {
        return Err(range("second"));
    }
    let mut nsec = 0;
    if v.len() >= 2 && matches!(v[0], b'.' | b',') && digit(v, 1) {
        let mut n = 2;
        while digit(v, n) {
            n += 1;
        }
        let digits = &v[1..n.min(10)];
        let scale = 10u32.pow(10 - n.min(10) as u32);
        nsec = std::str::from_utf8(digits).unwrap_or("0").parse::<u32>().unwrap_or(0) * scale;
        v = &v[n..];
    }
    // Z07:00
    let offset = if v.first() == Some(&b'Z') {
        v = &v[1..];
        0
    } else {
        let hold = v;
        if v.len() < 6 || v[3] != b':' {
            return Err(cannot("Z07:00", hold));
        }
        // the minutes read once the hours are, the ranges told before a number or a sign that does not read
        let hh = num(&v[1..3], true);
        let mm = hh.and_then(|_| num(&v[4..6], true));
        let (h, m) = (hh.map_or(0, |n| n.0), mm.map_or(0, |n| n.0));
        let sign = v[0];
        v = &v[6..];
        if m > 60 {
            return Err(range("time zone offset minute"));
        }
        if h > 24 {
            return Err(range("time zone offset hour"));
        }
        let off = (h * 60 + m) as i32 * 60;
        match (sign, mm.is_some()) {
            (b'+', true) => off,
            (b'-', true) => -off,
            _ => return Err(cannot("Z07:00", hold)),
        }
    };
    if !v.is_empty() {
        return Err(format!(
            "parsing time {}: extra text: {}",
            time_quote(value),
            time_quote(v)
        ));
    }
    let leap = year % 4 == 0 && (year % 100 != 0 || year % 400 == 0);
    let days = match month {
        2 if leap => 29,
        2 => 28,
        m => 30 + ((m + (m >> 3)) & 1),
    };
    if day < 1 || day > days {
        return Err(range("day"));
    }
    let t = NaiveDate::from_ymd_opt(year, month, day)
        .and_then(|d| d.and_hms_nano_opt(hour, min, sec, nsec))
        .zip(FixedOffset::east_opt(offset))
        .and_then(|(t, z)| t.and_local_timezone(z).single());
    Ok(t)
}

/// A text as the time package's `quote` writes it: a byte outside printable ASCII as `\xNN`.
fn time_quote(s: &[u8]) -> String {
    let mut out = String::from("\"");
    for &c in s {
        match c {
            b'"' | b'\\' => {
                out.push('\\');
                out.push(c as char);
            }
            c if !(b' '..0x80).contains(&c) => out.push_str(&format!("\\x{c:02x}")),
            c => out.push(c as char),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tells_valid_texts_as_go_does() {
        for ok in [&b"1"[..], b" {\"a\": [1, 2]} ", b"\"\xff\"", b"null\n"] {
            assert!(valid(ok), "{ok:?}");
        }
        for bad in [&b""[..], b" ", b"1 2", b"{\"a\":\"\\\xff\"}", b"\xff", b"[1,]"] {
            assert!(!valid(bad), "{bad:?}");
        }
    }

    static INNER: Struct = Struct {
        name: "Inner",
        go: "pkg.Inner",
        fields: &[("n", Ty::Int), ("m", Ty::Map)],
    };
    static ROOT: Struct = Struct {
        name: "Root",
        go: "pkg.Root",
        fields: &[
            ("s", Ty::Str),
            ("at", Ty::Time),
            ("xs", Ty::Slice(&Ty::Struct(&INNER), "[]pkg.Inner")),
            ("p", Ty::Ptr(&INNER)),
        ],
    };

    fn err(src: &str) -> String {
        match decode(src.as_bytes(), &ROOT) {
            Err(Fail::Go(e)) => e,
            other => panic!("{src}: {other:?}"),
        }
    }

    #[test]
    fn says_what_go_says() {
        assert_eq!(err("not json"), "invalid character 'o' in literal null (expecting 'u')");
        assert_eq!(err("  "), "EOF");
        assert_eq!(err("{\"s\": \"a"), "unexpected EOF");
        assert_eq!(err("{\"s\" 1}"), "invalid character '1' after object key");
        assert_eq!(err("{\"s\": 1 2}"), "invalid character '2' after object key:value pair");
        assert_eq!(err("[1 2]"), "invalid character '2' after array element");
        assert_eq!(
            err("{'s': 1}"),
            "invalid character '\\'' looking for beginning of object key string"
        );
        assert_eq!(
            err("{\"s\": 1.}"),
            "invalid character '}' after decimal point in numeric literal"
        );
        assert_eq!(err("{\"s\": \"\\q\"}"), "invalid character 'q' in string escape code");
        assert_eq!(
            err("{\"s\": é}"),
            "invalid character 'Ã' looking for beginning of value"
        );
        assert_eq!(
            err("[1]"),
            "json: cannot unmarshal array into Go value of type pkg.Root"
        );
        assert_eq!(err("{\"x\": 1, \"s\": 2}"), "json: unknown field \"x\"");
        assert_eq!(
            err("{\"s\": 2, \"x\": 1}"),
            "json: cannot unmarshal number into Go struct field Root.s of type string"
        );
        assert_eq!(
            err("{\"xs\": [{\"n\": 1.5}]}"),
            "json: cannot unmarshal number 1.5 into Go struct field Inner.xs.n of type int"
        );
        assert_eq!(
            err("{\"p\": [], \"at\": 1}"),
            "Time.UnmarshalJSON: input is not a JSON string"
        );
        assert_eq!(
            err("{\"at\": \"2026-02-30T00:00:00Z\"}"),
            "parsing time \"2026-02-30T00:00:00Z\": day out of range"
        );
        assert_eq!(
            err("{\"at\": \"x\"}"),
            "parsing time \"x\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"x\" as \"2006\""
        );
        let v = decode(
            b"{\"S\": \"a\", \"s\": null, \"at\": \"2026-01-02T3:04:05,5+02:00\", \"p\": null} tail",
            &ROOT,
        );
        assert_eq!(
            v.unwrap(),
            serde_json::json!({"s": "a", "at": "2026-01-02T03:04:05.500+02:00"})
        );
    }
}
