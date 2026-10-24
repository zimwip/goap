//! The value of an attribute of type code (`contracts/codetext`): a front matter giving at least its language, then
//! the text as a fenced Markdown code block. Only what the graph reads of it: `Parse` (`Check`) and `Block`.

/// A code value read from its text.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Value {
    pub language: String,
    pub code: String,
}

fn key_ok(k: &str) -> bool {
    let mut c = k.chars();
    c.next().is_some_and(|f| f.is_ascii_alphabetic())
        && c.all(|c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | '-'))
}

/// Reads a code value as `codetext.Parse` does, with its errors.
pub fn parse(s: &str) -> Result<Value, String> {
    let text = s.replace("\r\n", "\n");
    let lines: Vec<&str> = text.split('\n').collect();
    let mut v = Value::default();
    let mut i = 0;
    if lines[0] == "---" {
        let mut closed = false;
        i = 1;
        while i < lines.len() {
            let line = lines[i];
            if line == "---" {
                closed = true;
                i += 1;
                break;
            }
            let t = line.trim();
            if t.is_empty() || t.starts_with('#') {
                i += 1;
                continue;
            }
            let (k, val) = line.split_once(':').unwrap_or((line, ""));
            if !line.contains(':') || !key_ok(k.trim()) {
                return Err(format!(
                    "front matter line {}: a key: value is expected, got {}",
                    i + 1,
                    go_quote(line)
                ));
            }
            let mut val = val.trim().to_string();
            if val.starts_with('"') {
                val = unquote(&val).ok_or_else(|| format!("front matter line {}: invalid syntax", i + 1))?;
            } else if val.len() >= 2 && val.starts_with('\'') && val.ends_with('\'') {
                val = val[1..val.len() - 1].replace("''", "'");
            }
            if k.trim() == "language" {
                v.language = val;
            }
            i += 1;
        }
        if !closed {
            return Err("the front matter is not closed by a --- line".into());
        }
        while i + 1 < lines.len() && lines[i].trim().is_empty() {
            i += 1;
        }
    }
    if i >= lines.len() {
        return Ok(v);
    }
    let rest = &lines[i..];
    let Some((c, n, info)) = open_fence(rest[0]) else {
        v.code = rest.join("\n");
        return Ok(v);
    };
    if v.language.is_empty() {
        v.language = info;
    }
    match (1..rest.len()).find(|&j| close_fence(rest[j], c, n)) {
        None => v.code = rest[1..].join("\n"),
        Some(end) => {
            v.code = rest[1..end].join("\n");
            if !rest[end + 1..].join("\n").trim().is_empty() {
                return Err("text after the code block".into());
            }
        }
    }
    Ok(v)
}

/// A double-quoted Go string literal's value (`strconv.Unquote`); `None` where Go answers `invalid syntax`.
fn unquote(s: &str) -> Option<String> {
    let body = s.strip_prefix('"')?.strip_suffix('"')?;
    if body.contains('\n') {
        return None;
    }
    let mut out = String::with_capacity(body.len());
    let mut chars = body.chars();
    let hex = |chars: &mut std::str::Chars, n: usize| -> Option<u32> {
        let mut v = 0;
        for _ in 0..n {
            v = v * 16 + chars.next()?.to_digit(16)?;
        }
        Some(v)
    };
    while let Some(c) = chars.next() {
        match c {
            '"' => return None,
            '\\' => {
                let e = chars.next()?;
                match e {
                    'a' => out.push('\u{7}'),
                    'b' => out.push('\u{8}'),
                    'f' => out.push('\u{c}'),
                    'n' => out.push('\n'),
                    'r' => out.push('\r'),
                    't' => out.push('\t'),
                    'v' => out.push('\u{b}'),
                    '\\' => out.push('\\'),
                    '"' => out.push('"'),
                    // a byte: valid as text only below 0x80
                    'x' => out.push(char::from_u32(hex(&mut chars, 2).filter(|&b| b < 0x80)?)?),
                    'u' => out.push(char::from_u32(hex(&mut chars, 4)?)?),
                    'U' => out.push(char::from_u32(hex(&mut chars, 8)?)?),
                    '0'..='7' => {
                        let mut v = e.to_digit(8)?;
                        for _ in 0..2 {
                            v = v * 8 + chars.next()?.to_digit(8)?;
                        }
                        out.push(char::from_u32(Some(v).filter(|&b| b < 0x80)?)?);
                    }
                    _ => return None,
                }
            }
            c => out.push(c),
        }
    }
    Some(out)
}

/// A text as Go's `%q` writes it (`strconv.Quote`).
pub fn go_quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
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
            c if (c as u32) < 0x20 || c as u32 == 0x7f => out.push_str(&format!("\\x{:02x}", c as u32)),
            c if (0x80..0xa0).contains(&(c as u32)) || c == '\u{ad}' || c == '\u{feff}' => {
                out.push_str(&format!("\\u{:04x}", c as u32))
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

fn open_fence(line: &str) -> Option<(u8, usize, String)> {
    let b = line.as_bytes();
    if b.len() < 3 || (b[0] != b'`' && b[0] != b'~') {
        return None;
    }
    let c = b[0];
    let n = b.iter().take_while(|&&x| x == c).count();
    if n < 3 {
        return None;
    }
    let info = line[n..].trim();
    if c == b'`' && info.contains('`') {
        return None;
    }
    Some((c, n, info.split(' ').next().unwrap_or("").to_string()))
}

fn close_fence(line: &str, c: u8, n: usize) -> bool {
    let line = line.trim_end_matches([' ', '\t']);
    line.len() >= n && line.bytes().all(|x| x == c)
}

/// The fenced code block of a value alone (`codetext.Block`).
pub fn block(v: &Value) -> String {
    let longest = v.code.split(|c| c != '`').map(str::len).max().unwrap_or(0);
    let fence = "`".repeat(3.max(longest + 1));
    let info = if v.language.contains([' ', '\t', '`', '\n']) {
        ""
    } else {
        &v.language
    };
    let mut b = format!("{fence}{info}\n");
    let code = v.code.strip_suffix('\n').unwrap_or(&v.code);
    if !code.is_empty() {
        b.push_str(code);
        b.push('\n');
    }
    b.push_str(&fence);
    b.push('\n');
    b
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_and_fences() {
        let v = parse("---\nlanguage: go\n---\n```go\nfunc F() {}\n```").unwrap();
        assert_eq!(v.language, "go");
        assert_eq!(block(&v), "```go\nfunc F() {}\n```\n");
        assert_eq!(
            parse("---\nlanguage: go\n").unwrap_err(),
            "the front matter is not closed by a --- line"
        );
        assert_eq!(parse("plain").unwrap().code, "plain");
        assert_eq!(parse("---\nlanguage: \"g\\x41\"\n---\nx").unwrap().language, "gA");
        assert_eq!(
            parse("---\nlanguage: \"g\\q\"\n---\nx").unwrap_err(),
            "front matter line 2: invalid syntax"
        );
        assert_eq!(
            parse("---\nno key\n---\nx").unwrap_err(),
            "front matter line 2: a key: value is expected, got \"no key\""
        );
        assert_eq!(parse("```\nx\n```\ny").unwrap_err(), "text after the code block");
    }
}
