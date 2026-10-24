//! The value of an attribute of type code (`contracts/codetext`): a front matter giving at least its language, then
//! the text as a fenced Markdown code block. Only what the graph reads of it: `Parse` and `Block`.

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

/// Reads a code value as `codetext.Parse` does; `None` where Go answers an error.
pub fn parse(s: &str) -> Option<Value> {
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
            let (k, val) = line.split_once(':')?;
            if !key_ok(k.trim()) {
                return None;
            }
            let mut val = val.trim().to_string();
            if val.starts_with('"') {
                val = serde_json::from_str::<String>(&val).ok()?;
            } else if val.len() >= 2 && val.starts_with('\'') && val.ends_with('\'') {
                val = val[1..val.len() - 1].replace("''", "'");
            }
            if k.trim() == "language" {
                v.language = val;
            }
            i += 1;
        }
        if !closed {
            return None;
        }
        while i + 1 < lines.len() && lines[i].trim().is_empty() {
            i += 1;
        }
    }
    if i >= lines.len() {
        return Some(v);
    }
    let rest = &lines[i..];
    let Some((c, n, info)) = open_fence(rest[0]) else {
        v.code = rest.join("\n");
        return Some(v);
    };
    if v.language.is_empty() {
        v.language = info;
    }
    match (1..rest.len()).find(|&j| close_fence(rest[j], c, n)) {
        None => v.code = rest[1..].join("\n"),
        Some(end) => {
            v.code = rest[1..end].join("\n");
            if !rest[end + 1..].join("\n").trim().is_empty() {
                return None;
            }
        }
    }
    Some(v)
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
        assert!(parse("---\nlanguage: go\n").is_none());
        assert_eq!(parse("plain").unwrap().code, "plain");
    }
}
