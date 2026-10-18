//! Go's `path.Match`: shell file name patterns (`*`, `?`, `[...]`, `\` escapes), `*` not crossing a `/`.

/// ErrBadPattern: the pattern is malformed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct BadPattern;

impl std::fmt::Display for BadPattern {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "syntax error in pattern")
    }
}

/// Whether the name matches the shell pattern.
pub(crate) fn path_match(pattern: &str, name: &str) -> Result<bool, BadPattern> {
    let mut pattern = pattern.as_bytes();
    let mut name = name.as_bytes();
    'pattern: while !pattern.is_empty() {
        let (star, chunk, rest) = scan_chunk(pattern);
        pattern = rest;
        if star && chunk.is_empty() {
            // trailing * matches rest of string unless it has a /
            return Ok(!name.contains(&b'/'));
        }
        // look for match at current position
        let (t, ok, err) = match_chunk(chunk, name);
        // if we're the last chunk, make sure we've exhausted the name otherwise we'll give a false result even if we
        // could still match using the star
        if ok && (t.is_empty() || !pattern.is_empty()) {
            name = t;
            continue;
        }
        if let Some(e) = err {
            return Err(e);
        }
        if star {
            // look for match skipping i+1 bytes; cannot skip /
            let mut i = 0;
            while i < name.len() && name[i] != b'/' {
                let (t, ok, err) = match_chunk(chunk, &name[i + 1..]);
                if ok {
                    // if we're the last chunk, make sure we exhausted the name
                    if pattern.is_empty() && !t.is_empty() {
                        i += 1;
                        continue;
                    }
                    name = t;
                    continue 'pattern;
                }
                if let Some(e) = err {
                    return Err(e);
                }
                i += 1;
            }
        }
        // before returning false with no error, check that the remainder of the pattern is syntactically valid
        while !pattern.is_empty() {
            let (_, chunk, rest) = scan_chunk(pattern);
            pattern = rest;
            if let (_, _, Some(e)) = match_chunk(chunk, b"") {
                return Err(e);
            }
        }
        return Ok(false);
    }
    Ok(name.is_empty())
}

fn scan_chunk(mut pattern: &[u8]) -> (bool, &[u8], &[u8]) {
    let mut star = false;
    while !pattern.is_empty() && pattern[0] == b'*' {
        pattern = &pattern[1..];
        star = true;
    }
    let mut inrange = false;
    let mut i = 0;
    while i < pattern.len() {
        match pattern[i] {
            b'\\' => {
                // error check handled in match_chunk: bad pattern
                if i + 1 < pattern.len() {
                    i += 1;
                }
            }
            b'[' => inrange = true,
            b']' => inrange = false,
            b'*' if !inrange => break,
            _ => {}
        }
        i += 1;
    }
    (star, &pattern[..i], &pattern[i..])
}

/// The first rune of s and its length (U+FFFD and 1 for an invalid byte), as utf8.DecodeRuneInString.
fn decode(s: &[u8]) -> (char, usize) {
    if s.is_empty() {
        return ('\u{FFFD}', 0);
    }
    for n in 1..=4.min(s.len()) {
        if let Ok(t) = std::str::from_utf8(&s[..n])
            && let Some(c) = t.chars().next()
        {
            return (c, n);
        }
    }
    ('\u{FFFD}', 1)
}

fn match_chunk<'s>(mut chunk: &[u8], mut s: &'s [u8]) -> (&'s [u8], bool, Option<BadPattern>) {
    // failed records whether the match has failed; after the match fails, the loop continues on processing chunk,
    // checking that the pattern is well-formed but no longer reading s
    let mut failed = false;
    while !chunk.is_empty() {
        if !failed && s.is_empty() {
            failed = true;
        }
        match chunk[0] {
            b'[' => {
                // character class
                let mut r = '\0';
                if !failed {
                    let (c, n) = decode(s);
                    r = c;
                    s = &s[n..];
                }
                chunk = &chunk[1..];
                // possibly negated
                let mut negated = false;
                if !chunk.is_empty() && chunk[0] == b'^' {
                    negated = true;
                    chunk = &chunk[1..];
                }
                // parse all ranges
                let mut matched = false;
                let mut nrange = 0;
                loop {
                    if !chunk.is_empty() && chunk[0] == b']' && nrange > 0 {
                        chunk = &chunk[1..];
                        break;
                    }
                    let lo;
                    (lo, chunk) = match get_esc(chunk) {
                        Ok(x) => x,
                        Err(e) => return (b"", false, Some(e)),
                    };
                    let mut hi = lo;
                    if chunk[0] == b'-' {
                        (hi, chunk) = match get_esc(&chunk[1..]) {
                            Ok(x) => x,
                            Err(e) => return (b"", false, Some(e)),
                        };
                    }
                    if lo <= r && r <= hi {
                        matched = true;
                    }
                    nrange += 1;
                }
                if matched == negated {
                    failed = true;
                }
            }
            b'?' => {
                if !failed {
                    if s[0] == b'/' {
                        failed = true;
                    }
                    let (_, n) = decode(s);
                    s = &s[n..];
                }
                chunk = &chunk[1..];
            }
            c => {
                let mut c = c;
                if c == b'\\' {
                    chunk = &chunk[1..];
                    if chunk.is_empty() {
                        return (b"", false, Some(BadPattern));
                    }
                    c = chunk[0];
                }
                if !failed {
                    if c != s[0] {
                        failed = true;
                    }
                    s = &s[1..];
                }
                chunk = &chunk[1..];
            }
        }
    }
    if failed {
        return (b"", false, None);
    }
    (s, true, None)
}

fn get_esc(mut chunk: &[u8]) -> Result<(char, &[u8]), BadPattern> {
    if chunk.is_empty() || chunk[0] == b'-' || chunk[0] == b']' {
        return Err(BadPattern);
    }
    if chunk[0] == b'\\' {
        chunk = &chunk[1..];
        if chunk.is_empty() {
            return Err(BadPattern);
        }
    }
    let (r, n) = decode(chunk);
    if r == '\u{FFFD}' && n == 1 {
        return Err(BadPattern);
    }
    let rest = &chunk[n..];
    if rest.is_empty() {
        return Err(BadPattern);
    }
    Ok((r, rest))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_match() {
        assert_eq!(path_match("impact:*", "impact:REQ-1"), Ok(true));
        assert_eq!(path_match("*", "description"), Ok(true));
        assert_eq!(path_match("*", "a/b"), Ok(false));
        assert_eq!(path_match("description", "title"), Ok(false));
        assert_eq!(path_match("a?c", "abc"), Ok(true));
        assert_eq!(path_match("[a-c]x", "bx"), Ok(true));
        assert_eq!(path_match("[^a-c]x", "bx"), Ok(false));
        assert_eq!(path_match("a*b*c", "axxbyyc"), Ok(true));
        assert_eq!(path_match("[", ""), Err(BadPattern));
        assert_eq!(path_match("a\\", "a"), Err(BadPattern));
        assert_eq!(path_match("x[", "y"), Err(BadPattern));
    }
}
