//! Go's regular expressions (`regexp`, RE2 syntax with Perl's flags) run by Rust's `regex`: a pattern is read as Go
//! reads it and written again for Rust, its Perl classes (`\d`, `\s`, `\w`, `\b`) ASCII as Go's are, `\Q…\E` and
//! octal escapes spelled out, a `{` opening no repetition a literal, each literal escaped; what Go refuses is refused
//! in Go's words (`error parsing regexp: <code>: `<expr>``).
use regex::Regex;

/// A compiled pattern and the text it was compiled from (what `%v` prints of a `*regexp.Regexp`).
#[derive(Debug)]
pub struct GoRegex {
    source: String,
    re: Regex,
}

impl GoRegex {
    /// `regexp.Compile`
    pub fn compile(src: &str) -> Result<GoRegex, String> {
        let rust = translate(src).map_err(|(code, expr)| format!("error parsing regexp: {code}: `{expr}`"))?;
        let re = Regex::new(&rust).map_err(|e| {
            let msg = e.to_string();
            let code = if msg.contains("Unicode") || msg.contains("class") {
                "invalid character class range"
            } else if msg.contains("size limit") {
                "expression too large"
            } else {
                "invalid or unsupported Perl syntax"
            };
            format!("error parsing regexp: {code}: `{src}`")
        })?;
        Ok(GoRegex {
            source: src.to_string(),
            re,
        })
    }

    pub fn source(&self) -> &str {
        &self.source
    }

    pub fn is_match(&self, s: &str) -> bool {
        self.re.is_match(s)
    }

    /// The submatches of the leftmost match (`FindStringSubmatch`), an unmatched group empty.
    pub fn submatches(&self, s: &str) -> Option<Vec<String>> {
        self.re.captures(s).map(|c| {
            c.iter()
                .map(|m| m.map(|m| m.as_str().to_string()).unwrap_or_default())
                .collect()
        })
    }
}

type Refusal = (&'static str, String);

fn lit(c: char) -> String {
    if c.is_ascii_alphanumeric() {
        c.to_string()
    } else {
        format!("\\x{{{:X}}}", c as u32)
    }
}

const DIGIT: &str = "0-9";
const WORD: &str = "0-9A-Za-z_";
const SPACE: &str = "\\t\\n\\x0C\\r ";

/// A Perl class as the inside of a Rust class, or a negated one as a nested class.
fn perl_class(c: char) -> Option<(bool, &'static str)> {
    Some(match c {
        'd' => (false, DIGIT),
        'D' => (true, DIGIT),
        'w' => (false, WORD),
        'W' => (true, WORD),
        's' => (false, SPACE),
        'S' => (true, SPACE),
        _ => return None,
    })
}

const POSIX: [&str; 14] = [
    "alnum", "alpha", "ascii", "blank", "cntrl", "digit", "graph", "lower", "print", "punct", "space", "upper", "word",
    "xdigit",
];

struct Reader<'a> {
    src: &'a str,
    c: Vec<(usize, char)>,
    i: usize,
}

impl Reader<'_> {
    fn peek(&self, k: usize) -> Option<char> {
        self.c.get(self.i + k).map(|&(_, c)| c)
    }

    fn at(&self, i: usize) -> usize {
        self.c.get(i).map(|&(b, _)| b).unwrap_or(self.src.len())
    }

    fn text(&self, from: usize, to: usize) -> String {
        self.src[self.at(from)..self.at(to)].to_string()
    }

    /// An escape that stands for one character (`parseEscape`), the reader past it.
    fn escape(&mut self) -> Result<char, Refusal> {
        let start = self.i;
        self.i += 1;
        let Some(c) = self.peek(0) else {
            return Err(("trailing backslash at end of expression", String::new()));
        };
        self.i += 1;
        let invalid = |r: &Reader| ("invalid escape sequence", r.text(start, r.i));
        match c {
            '1'..='7' if !self.peek(0).is_some_and(|d| ('0'..='7').contains(&d)) => Err(invalid(self)),
            '0'..='7' => {
                let mut v = c as u32 - '0' as u32;
                for _ in 0..2 {
                    match self.peek(0) {
                        Some(d @ '0'..='7') => {
                            v = v * 8 + (d as u32 - '0' as u32);
                            self.i += 1;
                        }
                        _ => break,
                    }
                }
                Ok(char::from_u32(v).unwrap_or('\0'))
            }
            'x' => {
                if self.peek(0) == Some('{') {
                    self.i += 1;
                    let mut v: u32 = 0;
                    let mut n = 0;
                    while let Some(d) = self.peek(0).and_then(|d| d.to_digit(16)) {
                        v = v.saturating_mul(16).saturating_add(d);
                        n += 1;
                        self.i += 1;
                    }
                    if n == 0 || self.peek(0) != Some('}') || v > 0x10FFFF {
                        if self.peek(0).is_some() {
                            self.i += 1;
                        }
                        return Err(invalid(self));
                    }
                    self.i += 1;
                    return char::from_u32(v).ok_or_else(|| invalid(self));
                }
                let (Some(a), Some(b)) = (
                    self.peek(0).and_then(|d| d.to_digit(16)),
                    self.peek(1).and_then(|d| d.to_digit(16)),
                ) else {
                    self.i = (self.i + 2).min(self.c.len());
                    return Err(invalid(self));
                };
                self.i += 2;
                Ok(char::from_u32(a * 16 + b).unwrap_or('\0'))
            }
            'a' => Ok('\x07'),
            'f' => Ok('\x0C'),
            'n' => Ok('\n'),
            'r' => Ok('\r'),
            't' => Ok('\t'),
            'v' => Ok('\x0B'),
            c if c.is_ascii() && !c.is_ascii_alphanumeric() => Ok(c),
            _ => Err(invalid(self)),
        }
    }

    /// A Unicode class `\pN`, `\p{Name}`, `\PN`, copied as is.
    fn unicode_class(&mut self) -> Result<String, Refusal> {
        let start = self.i;
        self.i += 2;
        match self.peek(0) {
            Some('{') => {
                let mut j = self.i;
                while self.c.get(j).is_some_and(|&(_, c)| c != '}') {
                    j += 1;
                }
                if j >= self.c.len() {
                    return Err(("invalid character class range", self.text(start, self.c.len())));
                }
                self.i = j + 1;
            }
            Some(_) => self.i += 1,
            None => return Err(("invalid character class range", self.text(start, self.i))),
        }
        Ok(self.text(start, self.i))
    }

    /// `parseClass`
    fn class(&mut self) -> Result<String, Refusal> {
        let start = self.i;
        self.i += 1;
        let mut out = String::from("[");
        if self.peek(0) == Some('^') {
            out.push('^');
            self.i += 1;
        }
        let mut first = true;
        loop {
            match self.peek(0) {
                None => return Err(("missing closing ]", self.text(start, self.c.len()))),
                Some(']') if !first => break,
                _ => {}
            }
            first = false;
            if self.peek(0) == Some('[') && self.peek(1) == Some(':') {
                let rest = &self.src[self.at(self.i + 2)..];
                if let Some(end) = rest.find(":]") {
                    let name = &rest[..end];
                    let plain = name.strip_prefix('^').unwrap_or(name);
                    let whole = format!("[:{name}:]");
                    if !POSIX.contains(&plain) {
                        return Err(("invalid character class range", whole));
                    }
                    out.push_str(&whole);
                    self.i += whole.chars().count();
                    continue;
                }
            }
            if self.peek(0) == Some('\\') {
                if matches!(self.peek(1), Some('p' | 'P')) {
                    out.push_str(&self.unicode_class()?);
                    continue;
                }
                if let Some((neg, set)) = self.peek(1).and_then(perl_class) {
                    self.i += 2;
                    if neg {
                        out.push_str(&format!("[^{set}]"));
                    } else {
                        out.push_str(set);
                    }
                    continue;
                }
            }
            let from = self.i;
            let lo = self.class_char(start)?;
            if self.peek(0) == Some('-') && self.peek(1).is_some_and(|c| c != ']') {
                self.i += 1;
                let hi = self.class_char(start)?;
                if hi < lo {
                    return Err(("invalid character class range", self.text(from, self.i)));
                }
                out.push_str(&format!("{}-{}", lit(lo), lit(hi)));
            } else {
                out.push_str(&lit(lo));
            }
        }
        self.i += 1;
        out.push(']');
        Ok(out)
    }

    fn class_char(&mut self, start: usize) -> Result<char, Refusal> {
        match self.peek(0) {
            None => Err(("missing closing ]", self.text(start, self.c.len()))),
            Some('\\') => self.escape(),
            Some(c) => {
                self.i += 1;
                Ok(c)
            }
        }
    }

    /// A repetition `{n}`, `{n,}`, `{n,m}` at the reader, its bounds and its length; none when `{` opens none.
    fn repeat(&self) -> Option<(u32, Option<u32>, usize)> {
        let mut j = self.i + 1;
        let num = |j: &mut usize| {
            let s = *j;
            while self.c.get(*j).is_some_and(|&(_, c)| c.is_ascii_digit()) {
                *j += 1;
            }
            (*j > s).then(|| self.text(s, *j).parse::<u32>().unwrap_or(u32::MAX))
        };
        let lo = num(&mut j)?;
        let hi = match self.c.get(j).map(|&(_, c)| c) {
            Some(',') => {
                j += 1;
                if self.c.get(j).map(|&(_, c)| c) == Some('}') {
                    None
                } else {
                    Some(num(&mut j)?)
                }
            }
            _ => Some(lo),
        };
        (self.c.get(j).map(|&(_, c)| c) == Some('}')).then_some((lo, hi, j + 1 - self.i))
    }
}

/// A Go pattern written for Rust, or what Go answers of it.
fn translate(src: &str) -> Result<String, Refusal> {
    let mut r = Reader {
        src,
        c: src.char_indices().collect(),
        i: 0,
    };
    let mut out = String::new();
    let mut depth = 0usize;
    // whether a repetition has something to repeat, and where the last repetition started
    let mut atom = false;
    let mut last_rep: Option<usize> = None;
    while let Some(c) = r.peek(0) {
        let start = r.i;
        match c {
            '*' | '+' | '?' | '{' => {
                let len = if c == '{' {
                    match r.repeat() {
                        Some((lo, hi, len)) => {
                            if lo > 1000 || hi.is_some_and(|h| h > 1000 || h < lo) {
                                return Err(("invalid repeat count", r.text(start, start + len)));
                            }
                            len
                        }
                        None => {
                            out.push_str(&lit('{'));
                            r.i += 1;
                            atom = true;
                            last_rep = None;
                            continue;
                        }
                    }
                } else {
                    1
                };
                let mut end = start + len;
                if r.c.get(end).map(|&(_, c)| c) == Some('?') {
                    end += 1;
                }
                if let Some(prev) = last_rep {
                    return Err(("invalid nested repetition operator", r.text(prev, end)));
                }
                if !atom {
                    return Err(("missing argument to repetition operator", r.text(start, end)));
                }
                out.push_str(&r.text(start, end));
                r.i = end;
                last_rep = Some(start);
                continue;
            }
            '(' => {
                if r.peek(1) == Some('?') {
                    let rest = &src[r.at(r.i + 2)..];
                    if let Some(named) = rest.strip_prefix("P<").or_else(|| rest.strip_prefix('<')) {
                        let open = rest.len() - named.len() + 2;
                        let Some(end) = named.find('>') else {
                            return Err(("invalid named capture", src[r.at(start)..].to_string()));
                        };
                        let name = &named[..end];
                        let whole = &src[r.at(start)..r.at(start) + open + end + 1];
                        if name.is_empty() || !name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_') {
                            return Err(("invalid named capture", whole.to_string()));
                        }
                        out.push_str(&format!("(?P<{name}>"));
                        r.i += whole.chars().count();
                        depth += 1;
                        atom = false;
                        last_rep = None;
                        continue;
                    }
                    let flags: String = rest.chars().take_while(|c| !matches!(c, ')' | ':')).collect();
                    let close = rest[flags.len()..].chars().next();
                    let valid = close.is_some()
                        && flags.chars().all(|c| matches!(c, 'i' | 'm' | 's' | 'U' | '-'))
                        && flags.matches('-').count() <= 1
                        && !flags.ends_with('-')
                        && !(close == Some(')') && flags.is_empty());
                    if !valid {
                        let bad = flags
                            .char_indices()
                            .find(|&(_, c)| !matches!(c, 'i' | 'm' | 's' | 'U' | '-'))
                            .map(|(i, c)| i + c.len_utf8())
                            .unwrap_or(flags.len() + close.map(char::len_utf8).unwrap_or(0));
                        return Err((
                            "invalid or unsupported Perl syntax",
                            src[r.at(start)..r.at(start) + 2 + bad].to_string(),
                        ));
                    }
                    let n = 2 + flags.chars().count() + 1;
                    out.push_str(&r.text(start, start + n));
                    r.i += n;
                    if close == Some(':') {
                        depth += 1;
                        atom = false;
                    }
                    last_rep = None;
                    continue;
                }
                out.push('(');
                r.i += 1;
                depth += 1;
                atom = false;
                last_rep = None;
                continue;
            }
            ')' => {
                if depth == 0 {
                    return Err(("unexpected )", src.to_string()));
                }
                depth -= 1;
                out.push(')');
                r.i += 1;
            }
            '|' => {
                out.push('|');
                r.i += 1;
                atom = false;
                last_rep = None;
                continue;
            }
            '[' => out.push_str(&r.class()?),
            '.' | '^' | '$' => {
                out.push(c);
                r.i += 1;
            }
            '\\' => match r.peek(1) {
                Some(p @ ('d' | 'D' | 'w' | 'W' | 's' | 'S')) => {
                    let (neg, set) = perl_class(p).expect("a Perl class");
                    out.push_str(&format!("[{}{set}]", if neg { "^" } else { "" }));
                    r.i += 2;
                }
                Some(b @ ('b' | 'B')) => {
                    out.push_str(&format!("(?-u:\\{b})"));
                    r.i += 2;
                }
                Some(a @ ('A' | 'z')) => {
                    out.push_str(&format!("\\{a}"));
                    r.i += 2;
                }
                Some('p' | 'P') => out.push_str(&r.unicode_class()?),
                Some('Q') => {
                    r.i += 2;
                    let rest = &src[r.at(r.i)..];
                    let (quoted, skip) = match rest.find("\\E") {
                        Some(e) => (&rest[..e], e + 2),
                        None => (rest, rest.len()),
                    };
                    out.extend(quoted.chars().map(lit));
                    r.i += src[r.at(r.i)..r.at(r.i) + skip].chars().count();
                    if !quoted.is_empty() {
                        atom = true;
                        last_rep = None;
                    }
                    continue;
                }
                _ => {
                    let c = r.escape()?;
                    out.push_str(&lit(c));
                }
            },
            c => {
                out.push_str(&lit(c));
                r.i += 1;
            }
        }
        atom = true;
        last_rep = None;
    }
    if depth > 0 {
        return Err(("missing closing )", src.to_string()));
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn err(p: &str) -> String {
        GoRegex::compile(p).err().unwrap_or_default()
    }

    #[test]
    fn reads_go_patterns() {
        let m = |p: &str, s: &str| GoRegex::compile(p).expect(p).is_match(s);
        assert!(m(r"^release-\d+$", "release-12"));
        assert!(!m(r"\d", "٣"));
        assert!(m(r"a{", "a{"));
        assert!(m(r"[[a]", "["));
        assert!(m(r"\Q.*\E", "x.*"));
        assert!(!m(r"\Q.*\E", "xx"));
        assert!(m(r"\101", "A"));
        assert!(m(r"(?i)abc", "ABC"));
        assert!(m(r"(?P<n>a)b", "ab"));
        assert!(m(r"[[:alpha:]]", "a"));
        assert_eq!(err("a("), "error parsing regexp: missing closing ): `a(`");
        assert_eq!(err("a)"), "error parsing regexp: unexpected ): `a)`");
        assert_eq!(err("[a"), "error parsing regexp: missing closing ]: `[a`");
        assert_eq!(
            err("*a"),
            "error parsing regexp: missing argument to repetition operator: `*`"
        );
        assert_eq!(
            err("a**"),
            "error parsing regexp: invalid nested repetition operator: `**`"
        );
        assert_eq!(
            err("a\\"),
            "error parsing regexp: trailing backslash at end of expression: ``"
        );
        assert_eq!(err("\\q"), "error parsing regexp: invalid escape sequence: `\\q`");
        assert_eq!(
            err("[z-a]"),
            "error parsing regexp: invalid character class range: `z-a`"
        );
        assert_eq!(err("a{1001}"), "error parsing regexp: invalid repeat count: `{1001}`");
    }
}
