//! cel-go's `parser.Unparse` over the expression of a parse without macros: the text of an expression, normalised
//! (spacing, quotes, parentheses only where the precedence needs them).
use cel::common::ast::{EntryExpr, Expr, IdedExpr, LiteralValue, operators};

/// The operators of cel-go with their display and precedence (a higher number binds less).
fn operator(f: &str) -> Option<(&'static str, u8)> {
    Some(match f {
        operators::CONDITIONAL => ("", 8),
        operators::LOGICAL_OR => ("||", 7),
        operators::LOGICAL_AND => ("&&", 6),
        operators::EQUALS => ("==", 5),
        operators::GREATER => (">", 5),
        operators::GREATER_EQUALS => (">=", 5),
        operators::IN | "in" => ("in", 5),
        operators::LESS => ("<", 5),
        operators::LESS_EQUALS => ("<=", 5),
        operators::NOT_EQUALS => ("!=", 5),
        operators::ADD => ("+", 4),
        operators::SUBSTRACT => ("-", 4),
        operators::DIVIDE => ("/", 3),
        operators::MODULO => ("%", 3),
        operators::MULTIPLY => ("*", 3),
        operators::LOGICAL_NOT => ("!", 2),
        operators::NEGATE => ("-", 2),
        operators::INDEX => ("", 1),
        operators::OPT_INDEX => ("", 1),
        operators::OPT_SELECT => ("", 1),
        _ => return None,
    })
}

fn precedence(f: &str) -> u8 {
    operator(f).map(|(_, p)| p).unwrap_or(0)
}

fn binary(f: &str) -> Option<&'static str> {
    match f {
        operators::LOGICAL_NOT
        | operators::NEGATE
        | operators::CONDITIONAL
        | operators::INDEX
        | operators::OPT_INDEX
        | operators::OPT_SELECT => None,
        _ => operator(f).map(|(d, _)| d),
    }
}

fn call_of(e: &IdedExpr) -> Option<&cel::common::ast::CallExpr> {
    match &e.expr {
        Expr::Call(c) => Some(c),
        _ => None,
    }
}

/// A call with two arguments or more.
fn is_complex(e: &IdedExpr) -> bool {
    call_of(e).is_some_and(|c| c.args.len() >= 2)
}

fn is_complex_with_respect_to(op: &str, e: &IdedExpr) -> bool {
    match call_of(e) {
        Some(c) if c.args.len() >= 2 => precedence(op) < precedence(&c.func_name),
        _ => false,
    }
}

fn is_same_precedence(op: &str, e: &IdedExpr) -> bool {
    call_of(e).is_some_and(|c| precedence(op) == precedence(&c.func_name))
}

fn is_binary_or_ternary(e: &IdedExpr) -> bool {
    if !is_complex(e) {
        return false;
    }
    let f = &call_of(e).expect("a call").func_name;
    binary(f).is_some() || is_same_precedence(operators::CONDITIONAL, e)
}

fn is_left_recursive(op: &str) -> bool {
    op != operators::LOGICAL_AND && op != operators::LOGICAL_OR
}

/// The text of an expression.
pub(crate) fn unparse(e: &IdedExpr) -> String {
    let mut out = String::new();
    visit(e, &mut out);
    out
}

fn nested(e: &IdedExpr, paren: bool, out: &mut String) {
    if paren {
        out.push('(');
    }
    visit(e, out);
    if paren {
        out.push(')');
    }
}

fn visit(e: &IdedExpr, out: &mut String) {
    match &e.expr {
        Expr::Unspecified => {}
        Expr::Ident(n) => out.push_str(n),
        Expr::Literal(l) => literal(l, out),
        Expr::Select(s) => {
            if s.test {
                out.push_str("has(");
            }
            nested(&s.operand, !s.test && is_binary_or_ternary(&s.operand), out);
            out.push('.');
            out.push_str(&s.field);
            if s.test {
                out.push(')');
            }
        }
        Expr::List(l) => {
            out.push('[');
            for (i, x) in l.elements.iter().enumerate() {
                if i > 0 {
                    out.push_str(", ");
                }
                if l.optional_indices.contains(&i) {
                    out.push('?');
                }
                visit(x, out);
            }
            out.push(']');
        }
        Expr::Map(m) => {
            out.push('{');
            for (i, entry) in m.entries.iter().enumerate() {
                if i > 0 {
                    out.push_str(", ");
                }
                if let EntryExpr::MapEntry(me) = &entry.expr {
                    if me.optional {
                        out.push('?');
                    }
                    visit(&me.key, out);
                    out.push_str(": ");
                    visit(&me.value, out);
                }
            }
            out.push('}');
        }
        Expr::Struct(s) => {
            out.push_str(&s.type_name);
            out.push('{');
            for (i, entry) in s.entries.iter().enumerate() {
                if i > 0 {
                    out.push_str(", ");
                }
                if let EntryExpr::StructField(f) = &entry.expr {
                    if f.optional {
                        out.push('?');
                    }
                    out.push_str(&f.field);
                    out.push_str(": ");
                    visit(&f.value, out);
                }
            }
            out.push('}');
        }
        // not produced by a parse without macros
        Expr::Comprehension(_) => out.push_str(&format!("#{}", e.id)),
        Expr::Call(c) => call(c, out),
    }
}

fn call(c: &cel::common::ast::CallExpr, out: &mut String) {
    let f = c.func_name.as_str();
    let args = &c.args;
    match f {
        operators::CONDITIONAL if args.len() == 3 => {
            for (i, a) in args.iter().enumerate() {
                if i == 1 {
                    out.push_str(" ? ");
                } else if i == 2 {
                    out.push_str(" : ");
                }
                let paren = is_same_precedence(operators::CONDITIONAL, a) || is_complex(a);
                nested(a, paren, out);
            }
        }
        operators::OPT_SELECT if args.len() == 2 => {
            nested(&args[0], is_binary_or_ternary(&args[0]), out);
            out.push_str(".?");
            if let Expr::Literal(LiteralValue::String(s)) = &args[1].expr {
                out.push_str(s.inner());
            } else {
                visit(&args[1], out);
            }
        }
        operators::INDEX | operators::OPT_INDEX if args.len() == 2 => {
            nested(&args[0], is_binary_or_ternary(&args[0]), out);
            out.push_str(if f == operators::INDEX { "[" } else { "[?" });
            visit(&args[1], out);
            out.push(']');
        }
        operators::LOGICAL_NOT | operators::NEGATE if args.len() == 1 => {
            out.push_str(operator(f).map(|(d, _)| d).unwrap_or(""));
            nested(&args[0], is_complex(&args[0]), out);
        }
        _ if binary(f).is_some() && args.len() == 2 => {
            let (lhs, rhs) = (&args[0], &args[1]);
            let lhs_paren = is_complex_with_respect_to(f, lhs);
            let mut rhs_paren = is_complex_with_respect_to(f, rhs);
            if !rhs_paren && is_left_recursive(f) {
                rhs_paren = is_same_precedence(f, rhs);
            }
            nested(lhs, lhs_paren, out);
            out.push(' ');
            out.push_str(binary(f).unwrap_or(f));
            out.push(' ');
            nested(rhs, rhs_paren, out);
        }
        _ => {
            if let Some(t) = &c.target {
                nested(t, is_binary_or_ternary(t), out);
                out.push('.');
            }
            out.push_str(f);
            out.push('(');
            for (i, a) in args.iter().enumerate() {
                if i > 0 {
                    out.push_str(", ");
                }
                visit(a, out);
            }
            out.push(')');
        }
    }
}

fn literal(l: &LiteralValue, out: &mut String) {
    match l {
        LiteralValue::Boolean(b) => out.push_str(if *b.inner() { "true" } else { "false" }),
        LiteralValue::Bytes(b) => {
            out.push_str("b\"");
            for byte in b.inner() {
                match byte {
                    b'"' => out.push_str("\\\""),
                    b'\\' => out.push_str("\\\\"),
                    0x20..=0x7e => out.push(*byte as char),
                    _ => out.push_str(&format!("\\x{byte:02x}")),
                }
            }
            out.push('"');
        }
        LiteralValue::Double(d) => out.push_str(&format_double(*d.inner())),
        LiteralValue::Int(i) => out.push_str(&i.inner().to_string()),
        LiteralValue::UInt(u) => out.push_str(&format!("{}u", u.inner())),
        LiteralValue::Null => out.push_str("null"),
        LiteralValue::String(s) => out.push_str(&go_quote(s.inner())),
    }
}

/// Go's `strconv.FormatFloat(f, 'g', -1, 64)`.
pub(crate) fn format_double(f: f64) -> String {
    if f.is_nan() {
        return "NaN".into();
    }
    if f.is_infinite() {
        return if f > 0.0 { "+Inf" } else { "-Inf" }.into();
    }
    if f == 0.0 {
        return if f.is_sign_negative() { "-0" } else { "0" }.into();
    }
    // shortest round-trip digits, in scientific form
    let sci = format!("{f:e}");
    let (mant, exp) = sci.split_once('e').expect("an exponent");
    let exp: i32 = exp.parse().expect("an exponent");
    if !(-4..6).contains(&exp) {
        let sign = if exp < 0 { '-' } else { '+' };
        let a = exp.abs();
        return format!("{mant}e{sign}{a:02}");
    }
    format!("{f}")
}

/// Go's `strconv.Quote`.
pub(crate) fn go_quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for ch in s.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c as u32 == 0x7f => {
                out.push_str(&format!("\\x{:02x}", c as u32))
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}
