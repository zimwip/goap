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
    /// an integer literal out of the range of an int64, its text
    Huge(String),
    /// a punctuation token: the grammar's dashes and arrow heads outside ASCII read as `-`, `<` and `>` within a
    /// pattern only (`DASH`, `LEFT_HEAD`, `RIGHT_HEAD`)
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
    lex(src).ok().filter(|l| l.stray.is_none()).map(|l| l.tokens)
}

/// The tokens of a query and where the first `@` within a name stands, the character the grammar's lexer refuses
/// (`token recognition error at: '@'`), skips, and goes on; the name is read here through it, as one.
pub struct Lexed {
    pub tokens: Vec<Token>,
    pub stray: Option<usize>,
}

/// What the lexer does not read: where, and why.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LexError {
    pub pos: usize,
    pub msg: String,
}

/// The white space of the grammar beyond ASCII (`WHITESPACE`).
const SPACES: [char; 19] = [
    '\u{1680}', '\u{180e}', '\u{2000}', '\u{2001}', '\u{2002}', '\u{2003}', '\u{2004}', '\u{2005}', '\u{2006}',
    '\u{2007}', '\u{2008}', '\u{2009}', '\u{200a}', '\u{2028}', '\u{2029}', '\u{205f}', '\u{3000}', '\u{00a0}',
    '\u{202f}',
];

/// The dashes of a pattern (`oC_Dash`), its arrow heads (`oC_LeftArrowHead`, `oC_RightArrowHead`), beyond ASCII.
pub const DASH: &str = "\u{2014}";
pub const LEFT_HEAD: &str = "\u{27e8}";
pub const RIGHT_HEAD: &str = "\u{27e9}";
const DASHES: [char; 11] = [
    '\u{00ad}', '\u{2010}', '\u{2011}', '\u{2012}', '\u{2013}', '\u{2014}', '\u{2015}', '\u{2212}', '\u{fe58}',
    '\u{fe63}', '\u{ff0d}',
];
const LEFT_HEADS: [char; 4] = ['\u{27e8}', '\u{3008}', '\u{fe64}', '\u{ff1c}'];
const RIGHT_HEADS: [char; 4] = ['\u{27e9}', '\u{3009}', '\u{fe65}', '\u{ff1e}'];

/// A connector punctuation (`\p{Pc}`): a name may start with one.
fn connector(c: char) -> bool {
    matches!(
        c,
        '_' | '\u{203f}' | '\u{2040}' | '\u{2054}' | '\u{fe33}' | '\u{fe34}' | '\u{fe4d}'..='\u{fe4f}' | '\u{ff3f}'
    )
}

/// A currency symbol (`\p{Sc}`): a name may go on with one.
fn currency(c: char) -> bool {
    matches!(c, '$' | '\u{a2}'..='\u{a5}' | '\u{58f}' | '\u{60b}' | '\u{7fe}' | '\u{7ff}' | '\u{9f2}' | '\u{9f3}'
        | '\u{9fb}' | '\u{af1}' | '\u{bf9}' | '\u{e3f}' | '\u{17db}' | '\u{20a0}'..='\u{20c0}' | '\u{a838}'
        | '\u{fdfc}' | '\u{fe69}' | '\u{ff04}' | '\u{ffe0}' | '\u{ffe1}' | '\u{ffe5}' | '\u{ffe6}'
        | '\u{11fdd}'..='\u{11fe0}' | '\u{1e2ff}' | '\u{1ecb0}')
}

/// Where a name starts (`IdentifierStart`: `ID_Start` or `Pc`) and goes on (`IdentifierPart`: `ID_Continue` or `Sc`).
fn name_start(c: char) -> bool {
    unicode_ident::is_xid_start(c) || connector(c)
}

fn name_part(c: char) -> bool {
    unicode_ident::is_xid_continue(c) || currency(c)
}

/// The text the grammar's lexer names when it refuses a character.
fn refused(pos: usize, c: char) -> LexError {
    LexError {
        pos,
        msg: format!("token recognition error at: '{c}'"),
    }
}

/// The tokens of a query, a `@` within a name read as a part of it; the character not read otherwise.
pub fn lex(src: &str) -> Result<Lexed, LexError> {
    let b = src.as_bytes();
    let mut stray = None;
    let mut out = Vec::new();
    let mut i = 0;
    let mut sp = false;
    while i < b.len() {
        let c = src[i..].chars().next().unwrap_or_default();
        // white space and comments
        if matches!(c, ' ' | '\t' | '\n' | '\u{0b}' | '\u{0c}' | '\r' | '\u{1c}'..='\u{1f}') || SPACES.contains(&c) {
            sp = true;
            i += c.len_utf8();
            continue;
        }
        if src[i..].starts_with("/*") {
            let Some(end) = src[i + 2..].find("*/") else {
                return Err(LexError {
                    pos: i,
                    msg: "a comment is not closed".into(),
                });
            };
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
        let tok = if name_start(c) {
            loop {
                i += src[i..].chars().next().map_or(0, char::len_utf8);
                while let Some(n) = src[i..].chars().next().filter(|n| name_part(*n)) {
                    i += n.len_utf8();
                }
                // `dv@Thing`: the grammar's lexer cuts `dv`, refuses the `@`, then reads `Thing`; read here as one
                // name, the tokens before the `@` are the grammar's as long as `dv` is no keyword
                let more = src[i..]
                    .strip_prefix('@')
                    .and_then(|r| r.chars().next())
                    .is_some_and(name_part);
                if !more {
                    break;
                }
                if Tok::Word(src[start..i].to_string()).is_keyword() {
                    return Err(refused(i, '@'));
                }
                stray.get_or_insert(i);
            }
            Tok::Word(src[start..i].to_string())
        } else if c == '`' {
            let mut name = String::new();
            while i < b.len() && b[i] == b'`' {
                let Some(end) = src[i + 1..].find('`') else {
                    return Err(LexError {
                        pos: i,
                        msg: "a name between backticks is not closed".into(),
                    });
                };
                name.push_str(&src[i + 1..i + 1 + end]);
                i += end + 2;
                if i < b.len() && b[i] == b'`' {
                    name.push('`'); // a doubled backtick
                }
            }
            Tok::Escaped(name)
        } else if c == '\'' || c == '"' {
            let (s, n) = string(&src[i..]).map_err(|msg| LexError { pos: i, msg })?;
            i += n;
            Tok::Str(s)
        } else if c.is_ascii_digit() || (c == '.' && b.get(i + 1).is_some_and(u8::is_ascii_digit)) {
            let (t, n) = number(&src[i..]);
            i += n;
            t
        } else if DASHES.contains(&c) {
            i += c.len_utf8();
            Tok::Punct(DASH)
        } else if LEFT_HEADS.contains(&c) {
            i += c.len_utf8();
            Tok::Punct(LEFT_HEAD)
        } else if RIGHT_HEADS.contains(&c) {
            i += c.len_utf8();
            Tok::Punct(RIGHT_HEAD)
        } else if let Some(p) = PUNCTS.iter().find(|p| src[i..].starts_with(**p)) {
            i += p.len();
            Tok::Punct(p)
        } else {
            return Err(refused(i, c));
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
    Ok(Lexed { tokens: out, stray })
}

/// A string literal at the start of `s`: its value and its length; why it is not the grammar's otherwise.
fn string(s: &str) -> Result<(String, usize), String> {
    let quote = s.as_bytes()[0];
    let b = s.as_bytes();
    let mut i = 1;
    let mut out = String::new();
    let mut raw_start = 1;
    while i < b.len() {
        match b[i] {
            q if q == quote => {
                out.push_str(&s[raw_start..i]);
                return Ok((out, i + 1));
            }
            b'\\' => {
                out.push_str(&s[raw_start..i]);
                let bad = || "a string holds an escape sequence the grammar does not read".to_string();
                let Some(&e) = b.get(i + 1) else {
                    return Err("a string is not closed".into());
                };
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
                        let digits = hex(8).or_else(|| hex(4)).ok_or_else(bad)?;
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
                    _ => return Err(bad()),
                };
                i += n;
                raw_start = i;
            }
            _ => i += 1,
        }
    }
    Err("a string is not closed".into())
}

/// A number literal at the start of `s`: an integer (decimal, `0x` hexadecimal, `0o` octal) or a decimal one, an
/// integer out of range of an int64 kept as its text.
fn number(s: &str) -> (Tok, usize) {
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
        return (int(&s[..j], i64::from_str_radix(&s[2..j], 16).ok()), j);
    }
    if s.starts_with("0o") && b.get(2).is_some_and(|c| (b'0'..=b'7').contains(c)) {
        let j = digits(2, |c| (b'0'..=b'7').contains(c));
        return (int(&s[..j], i64::from_str_radix(&s[2..j], 8).ok()), j);
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
        return (Tok::Real(s[..end].to_string()), end);
    }
    // a decimal integer: 0, or digits not starting with 0
    if b[0] == b'0' {
        return (Tok::Int(0), 1);
    }
    (int(&s[..int_end], s[..int_end].parse().ok()), int_end)
}

fn int(text: &str, v: Option<i64>) -> Tok {
    v.map_or_else(|| Tok::Huge(text.to_string()), Tok::Int)
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
        assert_eq!(lex("(n:IN@x)").err().map(|e| e.pos), Some(5));
        assert!(tokens("'\\q'").is_none());
        // the grammar's names, white space, dashes and arrow heads beyond ASCII
        assert_eq!(
            toks("été a$b"),
            vec![Tok::Word("été".into()), Tok::Word("a$b".into()), Tok::Eof]
        );
        assert_eq!(
            toks("\u{a0}\u{27e8}\u{2212}"),
            vec![Tok::Punct(LEFT_HEAD), Tok::Punct(DASH), Tok::Eof]
        );
        assert_eq!(
            toks("99999999999999999999")[0],
            Tok::Huge("99999999999999999999".into())
        );
        assert_eq!(toks("``")[0], Tok::Escaped(String::new()));
        assert_eq!(
            lex("1 €").err().map(|e| e.msg),
            Some("token recognition error at: '€'".into())
        );
    }
}
