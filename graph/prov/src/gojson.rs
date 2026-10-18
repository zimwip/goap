//! JSON as Go's `encoding/json` writes it: object keys sorted, `<`, `>`, `&`, U+2028 and U+2029 escaped, floats in
//! Go's shortest form (`1e+21`, `1e-7`), and `MarshalIndent`'s layout.

use serde_json::{Number, Value};

/// `json.Marshal(v)`.
pub fn marshal(v: &Value) -> String {
    let mut out = String::new();
    write(&mut out, v, None, 0);
    out
}

/// `json.MarshalIndent(v, prefix, indent)`.
pub fn marshal_indent(v: &Value, prefix: &str, indent: &str) -> String {
    let mut out = String::new();
    write(&mut out, v, Some((prefix, indent)), 0);
    out
}

fn newline(out: &mut String, ind: Option<(&str, &str)>, depth: usize) {
    if let Some((prefix, indent)) = ind {
        out.push('\n');
        out.push_str(prefix);
        for _ in 0..depth {
            out.push_str(indent);
        }
    }
}

fn write(out: &mut String, v: &Value, ind: Option<(&str, &str)>, depth: usize) {
    match v {
        Value::Null => out.push_str("null"),
        Value::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
        Value::Number(n) => number(out, n),
        Value::String(s) => string(out, s),
        Value::Array(a) => {
            if a.is_empty() {
                out.push_str("[]");
                return;
            }
            out.push('[');
            for (i, x) in a.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                newline(out, ind, depth + 1);
                write(out, x, ind, depth + 1);
            }
            newline(out, ind, depth);
            out.push(']');
        }
        Value::Object(m) => {
            if m.is_empty() {
                out.push_str("{}");
                return;
            }
            let mut keys: Vec<&String> = m.keys().collect();
            keys.sort();
            out.push('{');
            for (i, k) in keys.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                newline(out, ind, depth + 1);
                string(out, k);
                out.push(':');
                if ind.is_some() {
                    out.push(' ');
                }
                write(out, &m[k.as_str()], ind, depth + 1);
            }
            newline(out, ind, depth);
            out.push('}');
        }
    }
}

fn number(out: &mut String, n: &Number) {
    if let Some(i) = n.as_i64() {
        out.push_str(&i.to_string());
    } else if let Some(u) = n.as_u64() {
        out.push_str(&u.to_string());
    } else if let Some(f) = n.as_f64() {
        out.push_str(&go_float(f));
    } else {
        out.push_str(&n.to_string());
    }
}

/// strconv.AppendFloat(f, 'f' or 'e', -1, 64) as encoding/json chooses and cleans it.
fn go_float(f: f64) -> String {
    let abs = f.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        let s = format!("{f:e}"); // "1.5e21", "1e-7"
        let (mant, exp) = s.split_once('e').unwrap_or((&s, "0"));
        let (sign, digits) = match exp.strip_prefix('-') {
            Some(d) => ('-', d),
            None => ('+', exp),
        };
        // Go writes at least two exponent digits, then encoding/json turns e-09 into e-9
        if sign == '-' {
            format!("{mant}e-{digits}")
        } else if digits.len() < 2 {
            format!("{mant}e+0{digits}")
        } else {
            format!("{mant}e+{digits}")
        }
    } else {
        format!("{f}")
    }
}

fn string(out: &mut String, s: &str) {
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn writes_as_go() {
        let v = json!({"b": [1, 2.5, 1e21, 1e-7, 0.000001], "a": "<&>\u{1}", "c": {}, "d": []});
        assert_eq!(
            marshal(&v),
            r#"{"a":"\u003c\u0026\u003e\u0001","b":[1,2.5,1e+21,1e-7,0.000001],"c":{},"d":[]}"#
        );
        assert_eq!(
            marshal_indent(&json!({"a": [1], "b": {}}), "", "  "),
            "{\n  \"a\": [\n    1\n  ],\n  \"b\": {}\n}"
        );
    }
}
