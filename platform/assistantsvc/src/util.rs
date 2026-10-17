//! Small helpers standing in for Go idioms: byte clipping, `encoding/json` output, `%v` of a JSON value, the loose
//! map reads of the actions and arguments.

use serde::Serialize;
use serde_json::{Map, Value};

/// The largest char boundary of s at or below n.
pub(crate) fn floor_boundary(s: &str, n: usize) -> usize {
    if n >= s.len() {
        return s.len();
    }
    let mut i = n;
    while !s.is_char_boundary(i) {
        i -= 1;
    }
    i
}

/// clip cuts s to n bytes (on a rune boundary) and marks the cut with an ellipsis.
pub(crate) fn clip(s: &str, n: usize) -> String {
    if s.len() <= n {
        return s.to_string();
    }
    format!("{}…", &s[..floor_boundary(s, n)])
}

/// go_json is `json.Marshal` of v: serde's output with the HTML characters and the line separators escaped as Go
/// does.
pub(crate) fn go_json<T: Serialize + ?Sized>(v: &T) -> String {
    let s = serde_json::to_string(v).unwrap_or_else(|_| "null".into());
    if !s.contains(['<', '>', '&', '\u{2028}', '\u{2029}']) {
        return s;
    }
    let mut out = String::with_capacity(s.len() + 16);
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}

/// go_v is `fmt.Sprintf("%v", v)` of a value read from a JSON map: a string as it is, a missing value `<nil>`.
pub(crate) fn go_v(v: Option<&Value>) -> String {
    match v {
        None | Some(Value::Null) => "<nil>".into(),
        Some(Value::String(s)) => s.clone(),
        Some(Value::Bool(b)) => b.to_string(),
        Some(Value::Number(n)) => n.to_string(),
        Some(Value::Array(l)) => format!(
            "[{}]",
            l.iter()
                .map(|e| go_v(Some(e)))
                .collect::<Vec<_>>()
                .join(" ")
        ),
        Some(Value::Object(m)) => format!(
            "map[{}]",
            m.iter()
                .map(|(k, e)| format!("{k}:{}", go_v(Some(e))))
                .collect::<Vec<_>>()
                .join(" ")
        ),
    }
}

/// sv is the string at key k of a map ("" when absent or not a string): Go's `a[k] == "x"` on a `map[string]any`.
pub(crate) fn sv<'a>(m: &'a Map<String, Value>, k: &str) -> &'a str {
    m.get(k).and_then(Value::as_str).unwrap_or("")
}

/// map_of is the object at key k of a map, empty when absent or not an object (Go's `mapOf`).
pub(crate) fn map_of(m: &Map<String, Value>, k: &str) -> Map<String, Value> {
    match m.get(k) {
        Some(Value::Object(o)) => o.clone(),
        _ => Map::new(),
    }
}

/// The arguments of a tool call.
#[derive(Debug, Clone, Default)]
pub(crate) struct Args(pub(crate) Map<String, Value>);

impl Args {
    /// str is the trimmed string argument k ("" when absent or not a string).
    pub(crate) fn str(&self, k: &str) -> String {
        sv(&self.0, k).trim().to_string()
    }

    /// integer reads a whole number argument (0 when absent).
    pub(crate) fn integer(&self, k: &str) -> Result<i64, String> {
        match self.0.get(k) {
            None | Some(Value::Null) => Ok(0),
            Some(Value::Number(n)) => {
                if let Some(i) = n.as_i64() {
                    return Ok(i);
                }
                match n.as_f64() {
                    Some(f) if f.fract() == 0.0 && f.abs() < 9.2e18 => Ok(f as i64),
                    _ => Err(format!("{k:?} must be a whole number")),
                }
            }
            Some(_) => Err(format!("{k:?} must be a whole number")),
        }
    }
}

/// q is Go's `%q` of a string (strconv.Quote).
pub(crate) fn q(s: &str) -> String {
    let mut o = String::with_capacity(s.len() + 2);
    o.push('"');
    for c in s.chars() {
        match c {
            '"' => o.push_str("\\\""),
            '\\' => o.push_str("\\\\"),
            '\n' => o.push_str("\\n"),
            '\t' => o.push_str("\\t"),
            '\r' => o.push_str("\\r"),
            '\u{7}' => o.push_str("\\a"),
            '\u{8}' => o.push_str("\\b"),
            '\u{c}' => o.push_str("\\f"),
            '\u{b}' => o.push_str("\\v"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => o.push_str(&format!("\\x{:02x}", c as u32)),
            c if c.is_control() => o.push_str(&format!("\\u{:04x}", c as u32)),
            c => o.push(c),
        }
    }
    o.push('"');
    o
}
