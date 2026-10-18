//! The canonical JSON text of a value, as Go's `encoding/json` writes it (maps with sorted keys, `<`, `>`, `&`,
//! U+2028 and U+2029 escaped, numbers as Go formats a float64): the text a fingerprint is computed over, so that it
//! does not depend on the encoder.

use serde_json::Value;

/// The canonical text of a value.
pub(crate) fn canonical(v: &Value) -> String {
    let mut out = String::new();
    write(&mut out, v);
    out
}

fn write(out: &mut String, v: &Value) {
    match v {
        Value::Null => out.push_str("null"),
        Value::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
        Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                out.push_str(&i.to_string());
            } else if let Some(u) = n.as_u64() {
                out.push_str(&u.to_string());
            } else {
                out.push_str(&go_float(n.as_f64().unwrap_or_default()));
            }
        }
        Value::String(s) => write_str(out, s),
        Value::Array(l) => {
            out.push('[');
            for (i, x) in l.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write(out, x);
            }
            out.push(']');
        }
        Value::Object(m) => {
            let mut keys: Vec<&String> = m.keys().collect();
            keys.sort();
            out.push('{');
            for (i, k) in keys.into_iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_str(out, k);
                out.push(':');
                write(out, &m[k]);
            }
            out.push('}');
        }
    }
}

/// A float64 as Go's encoding/json writes it: the shortest representation, in exponent form under 1e-6 and from
/// 1e21 on (`1e-7`, `1e+21`).
fn go_float(f: f64) -> String {
    if !f.is_finite() {
        return "null".into();
    }
    let abs = f.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        let s = format!("{f:e}");
        match s.split_once('e') {
            Some((m, e)) if !e.starts_with('-') => format!("{m}e+{e}"),
            _ => s,
        }
    } else {
        format!("{f}")
    }
}

fn write_str(out: &mut String, s: &str) {
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
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                out.push_str(&format!("\\u{:04x}", c as u32))
            }
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
        let v = json!({"b": [1, 2.5, 1e-7, 1e21, 100.0], "a": "x>y & <z>\n\u{2028}\u{1}", "c": null, "d": true});
        // the text Go's json.Marshal gives of the same map[string]any
        assert_eq!(
            canonical(&v),
            "{\"a\":\"x\\u003ey \\u0026 \\u003cz\\u003e\\n\\u2028\\u0001\",\"b\":[1,2.5,1e-7,1e+21,100],\"c\":null,\"d\":true}"
        );
    }
}
