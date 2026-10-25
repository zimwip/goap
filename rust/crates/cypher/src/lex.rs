//! The tokens of a query, as the lexer of the openCypher grammar (`lib/cypher/Cypher.g4`) cuts them: white space and
//! comments are one token, `SP`, which the grammar asks for or allows at given places only, so each token says
//! whether one precedes it. What this lexer does not read with certainty (a character outside ASCII outside a string)
//! is refused: the Go service then answers in the grammar's words. A `@` within a name (`alm@Requirement` written
//! without backticks) is the one character the grammar's lexer skips that is read here: [`lex`] says where it stands,
//! and the name goes on through it.

/// A token.
#[derive(Debug, Clone, PartialEq)]
pub enum Tok {
    /// a name: a keyword or a symbolic name, as written
    Word(String),
    /// a name between backticks, its doubled backticks read
    Escaped(String),
    /// a string literal, its escapes read
    Str(String),
    /// an integer literal, its value
    Int(i64),
    /// a decimal literal, its text
    Real(String),
    /// a punctuation token
    Punct(&'static str),
    Eof,
}

/// A token, where it starts and ends in the query, and whether white space precedes it.
#[derive(Debug, Clone)]
pub struct Token {
    pub tok: Tok,
    pub start: usize,
    pub end: usize,
    pub sp: bool,
}

/// The punctuation of the grammar, the longest first.
const PUNCTS: [&str; 26] = [
    "+=", "<>", "<=", ">=", "..", ";", ",", "=", "*", "(", ")", "[", "]", ":", "|", "<", ">", "+", "-", "/", "%", "^",
    ".", "{", "}", "$",
];

/// The keywords of the grammar (its lexer tokens spelled as words): none of them is a variable.
pub const KEYWORDS: [&str; 61] = [
    "UNION",
    "ALL",
    "OPTIONAL",
    "MATCH",
    "UNWIND",
    "AS",
    "MERGE",
    "ON",
    "CREATE",
    "SET",
    "DETACH",
    "DELETE",
    "REMOVE",
    "CALL",
    "YIELD",
    "WITH",
    "RETURN",
    "DISTINCT",
    "ORDER",
    "BY",
    "SKIP",
    "LIMIT",
    "ASCENDING",
    "ASC",
    "DESCENDING",
    "DESC",
    "WHERE",
    "OR",
    "XOR",
    "AND",
    "NOT",
    "STARTS",
    "ENDS",
    "CONTAINS",
    "IN",
    "IS",
    "NULL",
    "COUNT",
    "CASE",
    "ELSE",
    "END",
    "WHEN",
    "THEN",
    "ANY",
    "NONE",
    "SINGLE",
    "EXISTS",
    "TRUE",
    "FALSE",
    "CONSTRAINT",
    "DO",
    "FOR",
    "REQUIRE",
    "UNIQUE",
    "MANDATORY",
    "SCALAR",
    "OF",
    "ADD",
    "DROP",
    "FILTER",
    "EXTRACT",
];

/// The tokens of a query; None when a character is not read here, a `@` within a name included.
pub fn tokens(src: &str) -> Option<Vec<Token>> {
    lex(src).filter(|l| l.stray.is_none()).map(|l| l.tokens)
}

/// The tokens of a query and where the first `@` within a name stands, the character the grammar's lexer refuses
/// (`token recognition error at: '@'`), skips, and goes on; the name is read here through it, as one.
pub struct Lexed {
    pub tokens: Vec<Token>,
    pub stray: Option<usize>,
}

/// The tokens of a query, a `@` within a name read as a part of it; None when a character is not read here (a `@`
/// after a keyword or before no name either).
pub fn lex(src: &str) -> Option<Lexed> {
    let b = src.as_bytes();
    let mut stray = None;
    let mut out = Vec::new();
    let mut i = 0;
    let mut sp = false;
    while i < b.len() {
        let c = b[i];
        // white space and comments
        if matches!(c, b' ' | b'\t' | b'\n' | 0x0b | 0x0c | b'\r' | 0x1c..=0x1f) {
            sp = true;
            i += 1;
            continue;
        }
        if src[i..].starts_with("/*") {
            let end = src[i + 2..].find("*/")?;
            i += 2 + end + 2;
            sp = true;
            continue;
        }
        if src[i..].starts_with("//") {
            i += src[i..].find('\n').map_or(b.len() - i, |n| n + 1);
            sp = true;
            continue;
        }
        let start = i;
        let tok = if c.is_ascii_alphabetic() || c == b'_' {
            loop {
                while i < b.len() && (b[i].is_ascii_alphanumeric() || b[i] == b'_') {
                    i += 1;
                }
                // `dv@Thing`: the grammar's lexer cuts `dv`, refuses the `@`, then reads `Thing`; read here as one
                // name, the tokens before the `@` are the grammar's as long as `dv` is no keyword
                let more = b.get(i + 1).is_some_and(|c| c.is_ascii_alphanumeric() || *c == b'_');
                if b.get(i) != Some(&b'@') || !more {
                    break;
                }
                if Tok::Word(src[start..i].to_string()).is_keyword() {
                    return None;
                }
                stray.get_or_insert(i);
                i += 1;
            }
            if i < b.len() && !b[i].is_ascii() {
                return None; // a name going on outside ASCII
            }
            Tok::Word(src[start..i].to_string())
        } else if c == b'`' {
            let mut name = String::new();
            while i < b.len() && b[i] == b'`' {
                let end = src[i + 1..].find('`')?;
                name.push_str(&src[i + 1..i + 1 + end]);
                i += end + 2;
                if i < b.len() && b[i] == b'`' {
                    name.push('`'); // a doubled backtick
                }
            }
            if name.is_empty() {
                return None;
            }
            Tok::Escaped(name)
        } else if c == b'\'' || c == b'"' {
            let (s, n) = string(&src[i..])?;
            i += n;
            Tok::Str(s)
        } else if c.is_ascii_digit() || (c == b'.' && b.get(i + 1).is_some_and(u8::is_ascii_digit)) {
            let (t, n) = number(&src[i..])?;
            i += n;
            t
        } else if !c.is_ascii() {
            return None;
        } else {
            let p = PUNCTS.iter().find(|p| src[i..].starts_with(**p))?;
            i += p.len();
            Tok::Punct(p)
        };
        out.push(Token { tok, start, end: i, sp });
        sp = false;
    }
    out.push(Token {
        tok: Tok::Eof,
        start: b.len(),
        end: b.len(),
        sp,
    });
    Some(Lexed { tokens: out, stray })
}

/// A string literal at the start of `s`: its value and its length; None when its escapes are not the grammar's.
fn string(s: &str) -> Option<(String, usize)> {
    let quote = s.as_bytes()[0];
    let b = s.as_bytes();
    let mut i = 1;
    let mut out = String::new();
    let mut raw_start = 1;
    while i < b.len() {
        match b[i] {
            q if q == quote => {
                out.push_str(&s[raw_start..i]);
                return Some((out, i + 1));
            }
            b'\\' => {
                out.push_str(&s[raw_start..i]);
                let e = *b.get(i + 1)?;
                let n = match e {
                    b'\\' | b'\'' | b'"' => {
                        out.push(e as char);
                        2
                    }
                    b'b' | b'B' => {
                        out.push('\u{8}');
                        2
                    }
                    b'f' | b'F' => {
                        out.push('\u{c}');
                        2
                    }
                    b'n' | b'N' => {
                        out.push('\n');
                        2
                    }
                    b'r' | b'R' => {
                        out.push('\r');
                        2
                    }
                    b't' | b'T' => {
                        out.push('\t');
                        2
                    }
                    b'u' | b'U' => {
                        let hex = |n: usize| {
                            s.get(i + 2..i + 2 + n)
                                .filter(|h| h.bytes().all(|c| c.is_ascii_hexdigit()))
                        };
                        // the lexer takes eight digits when there are, else four
                        let digits = hex(8).or_else(|| hex(4))?;
                        // Go reads the code point, else keeps the `u` and the digits as they are
                        match u32::from_str_radix(digits, 16).ok().and_then(char::from_u32) {
                            Some(c) => out.push(c),
                            None => {
                                out.push(e as char);
                                out.push_str(digits);
                            }
                        }
                        2 + digits.len()
                    }
                    _ => return None,
                };
                i += n;
                raw_start = i;
            }
            _ => i += 1,
        }
    }
    None
}

/// A number literal at the start of `s`: an integer (decimal, `0x` hexadecimal, `0o` octal) or a decimal one; None
/// for an integer Go does not read.
fn number(s: &str) -> Option<(Tok, usize)> {
    let b = s.as_bytes();
    let digits = |from: usize, ok: fn(&u8) -> bool| {
        let mut j = from;
        while j < b.len() && ok(&b[j]) {
            j += 1;
        }
        j
    };
    if s.starts_with("0x") && b.get(2).is_some_and(u8::is_ascii_hexdigit) {
        let j = digits(2, u8::is_ascii_hexdigit);
        return Some((Tok::Int(i64::from_str_radix(&s[2..j], 16).ok()?), j));
    }
    if s.starts_with("0o") && b.get(2).is_some_and(|c| (b'0'..=b'7').contains(c)) {
        let j = digits(2, |c| (b'0'..=b'7').contains(c));
        return Some((Tok::Int(i64::from_str_radix(&s[2..j], 8).ok()?), j));
    }
    let int_end = digits(0, u8::is_ascii_digit);
    // a decimal part: `.` and digits (not `..`)
    let mut end = int_end;
    let mut real = false;
    if b.get(end) == Some(&b'.') && b.get(end + 1).is_some_and(u8::is_ascii_digit) {
        end = digits(end + 1, u8::is_ascii_digit);
        real = true;
    }
    // an exponent: `e`, an optional minus, digits
    if matches!(b.get(end), Some(b'e' | b'E')) {
        let mut j = end + 1;
        if b.get(j) == Some(&b'-') {
            j += 1;
        }
        if b.get(j).is_some_and(u8::is_ascii_digit) {
            end = digits(j, u8::is_ascii_digit);
            real = true;
        }
    }
    if real {
        return Some((Tok::Real(s[..end].to_string()), end));
    }
    // a decimal integer: 0, or digits not starting with 0
    if b[0] == b'0' {
        return Some((Tok::Int(0), 1));
    }
    Some((Tok::Int(s[..int_end].parse().ok()?), int_end))
}

impl Tok {
    /// Whether the token is the keyword `kw` (any case).
    pub fn is(&self, kw: &str) -> bool {
        matches!(self, Tok::Word(w) if w.eq_ignore_ascii_case(kw))
    }

    /// Whether the token is a keyword of the grammar.
    pub fn is_keyword(&self) -> bool {
        matches!(self, Tok::Word(w) if KEYWORDS.iter().any(|k| w.eq_ignore_ascii_case(k)))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn toks(s: &str) -> Vec<Tok> {
        tokens(s).unwrap().into_iter().map(|t| t.tok).collect()
    }

    #[test]
    fn cuts_numbers_strings_and_names() {
        assert_eq!(
            toks("x[1..2] 0x1F 1.5e-3 .5 'a\\nb' `a``b`"),
            vec![
                Tok::Word("x".into()),
                Tok::Punct("["),
                Tok::Int(1),
                Tok::Punct(".."),
                Tok::Int(2),
                Tok::Punct("]"),
                Tok::Int(31),
                Tok::Real("1.5e-3".into()),
                Tok::Real(".5".into()),
                Tok::Str("a\nb".into()),
                Tok::Escaped("a`b".into()),
                Tok::Eof,
            ]
        );
        assert_eq!(toks("$p")[0], Tok::Punct("$"));
        assert_eq!(lex("(n:dv@Thing)").unwrap().stray, Some(5));
        assert!(tokens("(n:dv@Thing)").is_none());
        assert!(lex("(n:IN@x)").is_none());
        assert!(tokens("'\\q'").is_none());
        assert!(tokens("été").is_none());
    }
}
