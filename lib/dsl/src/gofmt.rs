//! Go's `fmt.Sprint` over the JSON values a script hands over (what goja's `Export` gave Go: strings, int64,
//! float64, bool, nil, `map[string]any`, `[]any`), for the console lines and the output of a script.

use serde_json::Value;

/// `fmt.Sprint(args...)`: the operands formatted with `%v`, a space between two operands when neither is a string.
pub fn sprint(args: &[Value]) -> String {
    let mut out = String::new();
    for (i, a) in args.iter().enumerate() {
        if i > 0 && !args[i - 1].is_string() && !a.is_string() {
            out.push(' ');
        }
        out.push_str(&value(a));
    }
    out
}

/// `%v` of one value.
pub(crate) fn value(v: &Value) -> String {
    match v {
        Value::Null => "<nil>".to_string(),
        Value::Bool(b) => b.to_string(),
        Value::String(s) => s.clone(),
        Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                i.to_string()
            } else if let Some(u) = n.as_u64() {
                u.to_string()
            } else {
                float(n.as_f64().unwrap_or(0.0))
            }
        }
        Value::Array(a) => {
            let parts: Vec<String> = a.iter().map(value).collect();
            format!("[{}]", parts.join(" "))
        }
        Value::Object(m) => {
            let parts: Vec<String> = m.iter().map(|(k, v)| format!("{k}:{}", value(v))).collect();
            format!("map[{}]", parts.join(" "))
        }
    }
}

/// `%v` of a float64 (`%g`, shortest): `%e` (two-digit exponent at least) when the exponent is below -4 or at least
/// 6, the shortest decimal otherwise.
fn float(f: f64) -> String {
    if f.is_nan() {
        return "NaN".to_string();
    }
    if f.is_infinite() {
        return if f > 0.0 { "+Inf" } else { "-Inf" }.to_string();
    }
    if f == 0.0 {
        return if f.is_sign_negative() { "-0" } else { "0" }.to_string();
    }
    let exp = f.abs().log10().floor() as i32;
    if !(-4..6).contains(&exp) {
        let e = format!("{f:e}");
        let (mantissa, exponent) = e.split_once('e').unwrap_or((&e, "0"));
        let (sign, digits) = match exponent.strip_prefix('-') {
            Some(d) => ('-', d),
            None => ('+', exponent),
        };
        return format!("{mantissa}e{sign}{digits:0>2}");
    }
    format!("{f}")
}

/// The JavaScript `ToString` of a value handed to a parameter of type string (`null` / `undefined`: "").
pub(crate) fn js_string(v: &Value) -> String {
    match v {
        Value::Null => String::new(),
        Value::Bool(b) => b.to_string(),
        Value::String(s) => s.clone(),
        Value::Number(n) => match n.as_i64() {
            Some(i) => i.to_string(),
            None => {
                let f = n.as_f64().unwrap_or(0.0);
                let exp = if f == 0.0 {
                    0
                } else {
                    f.abs().log10().floor() as i32
                };
                if !(-7..21).contains(&exp) {
                    let e = format!("{f:e}");
                    match e.split_once('e') {
                        Some((m, x)) if !x.starts_with('-') => format!("{m}e+{x}"),
                        _ => e,
                    }
                } else {
                    format!("{f}")
                }
            }
        },
        Value::Array(a) => a.iter().map(js_string).collect::<Vec<_>>().join(","),
        Value::Object(_) => "[object Object]".to_string(),
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn sprint_spaces_between_non_strings() {
        assert_eq!(
            sprint(&[json!("tokens"), json!(10), json!(2)]),
            "tokens10 2"
        );
        assert_eq!(sprint(&[json!("a"), json!("b")]), "ab");
        assert_eq!(
            sprint(&[json!(1), json!(true), Value::Null]),
            "1 true <nil>"
        );
        assert_eq!(
            sprint(&[json!({"b": 1, "a": [1, "x"]})]),
            "map[a:[1 x] b:1]"
        );
    }

    #[test]
    fn floats_like_go() {
        assert_eq!(value(&json!(1.5)), "1.5");
        assert_eq!(value(&json!(0.0001)), "0.0001");
        assert_eq!(value(&json!(0.00001)), "1e-05");
        assert_eq!(value(&json!(1e21)), "1e+21");
        assert_eq!(value(&json!(1234567.5)), "1.2345675e+06");
        assert_eq!(js_string(&json!(0.5)), "0.5");
        assert_eq!(js_string(&json!(1e21)), "1e+21");
        assert_eq!(js_string(&json!([1, "a"])), "1,a");
    }
}
