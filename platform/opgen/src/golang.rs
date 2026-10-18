//! The Go side of the scan: a small lexer over the Go sources, enough to find the function declarations, their doc
//! comments, their receivers and the `allow` calls of their bodies (what `go/parser` gave the Go generator).
use std::{fs, path::Path};

use crate::opgen::{
    DIRECTIVE, Error, Found, Operation, RECEIVER, Source, first_sentence, go_quote, valid_code,
    valid_kind,
};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Kind {
    Ident,
    Punct,
    Literal,
    Comment,
}

#[derive(Debug, Clone)]
struct Tok {
    kind: Kind,
    text: String,
    line: usize,
    end_line: usize,
}

fn lex(src: &str, file: &str) -> Result<Vec<Tok>, Error> {
    let b: Vec<char> = src.chars().collect();
    let mut toks = Vec::new();
    let (mut i, mut line) = (0usize, 1usize);
    let unterminated =
        |what: &str, line: usize| Error::Parse(format!("{file}:{line}: {what} not terminated"));
    while i < b.len() {
        let c = b[i];
        if c == '\n' {
            line += 1;
            i += 1;
            continue;
        }
        if c.is_whitespace() {
            i += 1;
            continue;
        }
        let start = i;
        let start_line = line;
        let kind;
        if c == '/' && b.get(i + 1) == Some(&'/') {
            while i < b.len() && b[i] != '\n' {
                i += 1;
            }
            kind = Kind::Comment;
        } else if c == '/' && b.get(i + 1) == Some(&'*') {
            i += 2;
            loop {
                if i + 1 >= b.len() {
                    return Err(unterminated("comment", start_line));
                }
                if b[i] == '*' && b[i + 1] == '/' {
                    i += 2;
                    break;
                }
                if b[i] == '\n' {
                    line += 1;
                }
                i += 1;
            }
            kind = Kind::Comment;
        } else if c == '"' || c == '\'' {
            i += 1;
            loop {
                match b.get(i) {
                    None | Some('\n') => return Err(unterminated("string literal", start_line)),
                    Some('\\') => i += 2,
                    Some(&q) if q == c => {
                        i += 1;
                        break;
                    }
                    _ => i += 1,
                }
            }
            kind = Kind::Literal;
        } else if c == '`' {
            i += 1;
            loop {
                match b.get(i) {
                    None => return Err(unterminated("raw string literal", start_line)),
                    Some('`') => {
                        i += 1;
                        break;
                    }
                    Some('\n') => {
                        line += 1;
                        i += 1;
                    }
                    _ => i += 1,
                }
            }
            kind = Kind::Literal;
        } else if c.is_ascii_digit() {
            while i < b.len() && (b[i].is_alphanumeric() || b[i] == '_' || b[i] == '.') {
                i += 1;
            }
            kind = Kind::Literal;
        } else if c.is_alphabetic() || c == '_' {
            while i < b.len() && (b[i].is_alphanumeric() || b[i] == '_') {
                i += 1;
            }
            kind = Kind::Ident;
        } else {
            i += 1;
            kind = Kind::Punct;
        }
        let mut text: String = b[start..i].iter().collect();
        if kind == Kind::Comment && text.starts_with("//") && text.ends_with('\r') {
            text.pop();
        }
        toks.push(Tok {
            kind,
            text,
            line: start_line,
            end_line: line,
        });
    }
    Ok(toks)
}

/// Whether a token ends a statement when a newline follows it (Go's automatic semicolons).
fn ends_statement(t: &Tok) -> bool {
    match t.kind {
        Kind::Literal => true,
        Kind::Ident => !matches!(
            t.text.as_str(),
            "func"
                | "type"
                | "var"
                | "const"
                | "import"
                | "package"
                | "interface"
                | "struct"
                | "map"
                | "chan"
                | "go"
                | "defer"
                | "if"
                | "else"
                | "for"
                | "switch"
                | "select"
                | "case"
                | "default"
                | "range"
        ),
        Kind::Punct => matches!(t.text.as_str(), ")" | "]" | "}"),
        Kind::Comment => false,
    }
}

/// `ast.isDirective`, the comment text without its `//`.
fn is_directive(c: &str) -> bool {
    if c.starts_with("line ") || c.starts_with("extern ") || c.starts_with("export ") {
        return true;
    }
    let b = c.as_bytes();
    let Some(colon) = c.find(':') else {
        return false;
    };
    if colon == 0 || colon + 1 >= b.len() {
        return false;
    }
    (0..=colon + 1)
        .filter(|&i| i != colon)
        .all(|i| b[i].is_ascii_lowercase() || b[i].is_ascii_digit())
}

/// `ast.CommentGroup.Text`, close enough for a first sentence: the comment markers and the directives removed.
fn doc_text(comments: &[&Tok]) -> String {
    let mut out = String::new();
    for c in comments {
        if let Some(rest) = c.text.strip_prefix("//") {
            if is_directive(rest) {
                continue;
            }
            out.push_str(rest);
        } else {
            let t = &c.text;
            out.push_str(&t[2..t.len() - 2]);
        }
        out.push('\n');
    }
    out
}

struct FuncDecl {
    /// The index of the `func` token.
    at: usize,
    /// The receiver tokens (inside the parentheses), none for a function.
    recv: Option<Vec<usize>>,
    name: Option<usize>,
    /// The token range of the body, braces included.
    body: Option<(usize, usize)>,
}

/// The index of the token closing the one opening at `open`, over the significant tokens `sig`.
fn matching(toks: &[Tok], sig: &[usize], open: usize) -> Option<usize> {
    let mut depth = 0i64;
    for (k, &i) in sig.iter().enumerate().skip(open) {
        match toks[i].text.as_str() {
            "(" | "[" | "{" if toks[i].kind == Kind::Punct => depth += 1,
            ")" | "]" | "}" if toks[i].kind == Kind::Punct => {
                depth -= 1;
                if depth == 0 {
                    return Some(k);
                }
            }
            _ => {}
        }
    }
    None
}

fn is_punct(t: &Tok, p: &str) -> bool {
    t.kind == Kind::Punct && t.text == p
}

/// Parses the declaration starting at the `func` of `sig[k]`; returns it and the position after it in `sig`.
fn func_decl(toks: &[Tok], sig: &[usize], k: usize) -> (FuncDecl, usize) {
    let mut d = FuncDecl {
        at: sig[k],
        recv: None,
        name: None,
        body: None,
    };
    let mut k = k + 1;
    if k < sig.len() && is_punct(&toks[sig[k]], "(") {
        let Some(close) = matching(toks, sig, k) else {
            return (d, sig.len());
        };
        d.recv = Some(sig[k + 1..close].to_vec());
        k = close + 1;
    }
    if k < sig.len() && toks[sig[k]].kind == Kind::Ident {
        d.name = Some(sig[k]);
        k += 1;
    }
    // type parameters, parameters, results, then the body
    let mut depth = 0i64;
    while k < sig.len() {
        let t = &toks[sig[k]];
        if depth == 0 && k > 0 {
            let prev = &toks[sig[k - 1]];
            if t.line > prev.end_line && ends_statement(prev) {
                return (d, k);
            }
        }
        if t.kind == Kind::Punct {
            match t.text.as_str() {
                "{" if depth == 0
                    && !(toks[sig[k - 1]].kind == Kind::Ident
                        && matches!(toks[sig[k - 1]].text.as_str(), "interface" | "struct")) =>
                {
                    let Some(close) = matching(toks, sig, k) else {
                        return (d, sig.len());
                    };
                    d.body = Some((sig[k], sig[close]));
                    return (d, close + 1);
                }
                "(" | "[" | "{" => depth += 1,
                ")" | "]" | "}" => depth -= 1,
                ";" if depth == 0 => return (d, k + 1),
                _ => {}
            }
        }
        k += 1;
    }
    (d, k)
}

/// The constants the calls `<x>.allow(_, _, operations.<Const>)` of a token range name, with their lines.
fn allows_in(toks: &[Tok], from: usize, to: usize) -> Vec<(String, usize)> {
    let sig: Vec<usize> = (from..=to)
        .filter(|&i| toks[i].kind != Kind::Comment)
        .collect();
    let mut out = Vec::new();
    for k in 0..sig.len() {
        if !(is_punct(&toks[sig[k]], ".")
            && sig.get(k + 2).is_some()
            && toks[sig[k + 1]].kind == Kind::Ident
            && toks[sig[k + 1]].text == "allow"
            && is_punct(&toks[sig[k + 2]], "("))
        {
            continue;
        }
        let Some(close) = matching(toks, &sig, k + 2) else {
            continue;
        };
        let mut args: Vec<Vec<usize>> = vec![Vec::new()];
        let mut depth = 0i64;
        for &i in &sig[k + 3..close] {
            let t = &toks[i];
            if t.kind == Kind::Punct {
                match t.text.as_str() {
                    "(" | "[" | "{" => depth += 1,
                    ")" | "]" | "}" => depth -= 1,
                    "," if depth == 0 => {
                        args.push(Vec::new());
                        continue;
                    }
                    _ => {}
                }
            }
            args.last_mut().unwrap().push(i);
        }
        if args.last().is_some_and(Vec::is_empty) && args.len() > 1 {
            args.pop();
        }
        if args.len() != 3 {
            continue;
        }
        let a = &args[2];
        if a.len() == 3
            && toks[a[0]].kind == Kind::Ident
            && toks[a[0]].text == "operations"
            && is_punct(&toks[a[1]], ".")
            && toks[a[2]].kind == Kind::Ident
        {
            let line = if k > 0 {
                toks[sig[k - 1]].line
            } else {
                toks[sig[k]].line
            };
            out.push((toks[a[2]].text.clone(), line));
        }
    }
    out
}

fn is_graph_method(toks: &[Tok], d: &FuncDecl) -> bool {
    let (Some(recv), Some(name)) = (&d.recv, d.name) else {
        return false;
    };
    if !toks[name]
        .text
        .chars()
        .next()
        .is_some_and(char::is_uppercase)
    {
        return false;
    }
    let r: Vec<(Kind, &str)> = recv
        .iter()
        .map(|&i| (toks[i].kind, toks[i].text.as_str()))
        .collect();
    matches!(
        r.as_slice(),
        [
            (Kind::Ident, _),
            (Kind::Punct, "*"),
            (Kind::Ident, RECEIVER)
        ] | [(Kind::Punct, "*"), (Kind::Ident, RECEIVER)]
    )
}

/// Reads the non-test Go files of dir.
pub(crate) fn scan(dir: &Path) -> Result<Found, Error> {
    let mut found = Found::default();
    let mut files: Vec<_> = match fs::read_dir(dir) {
        Ok(rd) => rd
            .filter_map(Result::ok)
            .map(|e| e.path())
            .filter(|p| {
                p.extension().is_some_and(|e| e == "go")
                    && !p.to_string_lossy().ends_with("_test.go")
                    && p.is_file()
            })
            .collect(),
        Err(e) => return Err(Error::Io(dir.display().to_string(), e)),
    };
    files.sort();
    for path in files {
        let name = path
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_default();
        let src =
            fs::read_to_string(&path).map_err(|e| Error::Io(path.display().to_string(), e))?;
        scan_file(&src, &name, &mut found)?;
    }
    Ok(found)
}

fn scan_file(src: &str, file: &str, found: &mut Found) -> Result<(), Error> {
    let toks = lex(src, file)?;
    let pos = |line: usize| format!("{file}:{line}");
    let sig: Vec<usize> = (0..toks.len())
        .filter(|&i| toks[i].kind != Kind::Comment)
        .collect();
    let mut claimed = vec![false; toks.len()];

    // the function declarations: a `func` at depth 0 starting a declaration
    let mut decls = Vec::new();
    let (mut k, mut depth) = (0usize, 0i64);
    while k < sig.len() {
        let t = &toks[sig[k]];
        let starts = depth == 0
            && t.kind == Kind::Ident
            && t.text == "func"
            && (k == 0 || {
                let prev = &toks[sig[k - 1]];
                is_punct(prev, ";") || (prev.end_line < t.line && ends_statement(prev))
            });
        if starts {
            let (d, next) = func_decl(&toks, &sig, k);
            decls.push(d);
            k = next.max(k + 1);
            continue;
        }
        if t.kind == Kind::Punct {
            match t.text.as_str() {
                "(" | "[" | "{" => depth += 1,
                ")" | "]" | "}" => depth -= 1,
                _ => {}
            }
        }
        k += 1;
    }

    for d in &decls {
        if let Some((from, to)) = d.body {
            for (name, line) in allows_in(&toks, from, to) {
                found.allows.entry(name).or_default().push(pos(line));
            }
        }
        // the doc: the comment group ending on the line before the func, after the previous token's line
        let mut group: Vec<usize> = Vec::new();
        let mut i = d.at;
        if i > 0 && toks[i - 1].kind == Kind::Comment && toks[i - 1].end_line + 1 == toks[d.at].line
        {
            loop {
                i -= 1;
                group.push(i);
                if !(i > 0
                    && toks[i - 1].kind == Kind::Comment
                    && toks[i - 1].end_line + 1 >= toks[i].line)
                {
                    break;
                }
            }
        }
        group.reverse();
        let prev_line = if i > 0 { toks[i - 1].end_line } else { 0 };
        group.retain(|&g| toks[g].line > prev_line);
        if group.is_empty() {
            continue;
        }
        let comments: Vec<&Tok> = group.iter().map(|&g| &toks[g]).collect();
        for &g in &group {
            let c = &toks[g];
            let Some(rest) = c.text.strip_prefix(DIRECTIVE) else {
                continue;
            };
            claimed[g] = true;
            let fields: Vec<&str> = rest.split_whitespace().collect();
            let at = pos(c.line);
            if fields.len() != 2 {
                found.errs.push(format!(
                    "{at}: want {DIRECTIVE} <code> <kind>, got {}",
                    go_quote(&c.text)
                ));
                continue;
            }
            if !valid_code(fields[0]) {
                found.errs.push(format!(
                    "{at}: operation code {}: lowercase dotted words, at least two (impact.review)",
                    go_quote(fields[0])
                ));
                continue;
            }
            if !valid_kind(fields[1]) {
                found.errs.push(format!(
                    "{at}: operation {}: unknown kind {} (doer or reviewer)",
                    fields[0],
                    go_quote(fields[1])
                ));
                continue;
            }
            if !is_graph_method(&toks, d) {
                found.errs.push(format!(
                    "{at}: operation {}: only an exported method of *{RECEIVER} performs an operation",
                    fields[0]
                ));
                continue;
            }
            let method = format!("{RECEIVER}.{}", toks[d.name.unwrap()].text);
            found.ops.push(Operation {
                code: fields[0].to_string(),
                kind: fields[1].to_string(),
                method,
                doc: first_sentence(&doc_text(&comments)),
                pos: at,
                source: Source::Go,
            });
            let names = d
                .body
                .map(|(from, to)| {
                    allows_in(&toks, from, to)
                        .into_iter()
                        .map(|a| a.0)
                        .collect()
                })
                .unwrap_or_default();
            found.bodies.insert(fields[0].to_string(), names);
        }
    }
    // a directive that documents no function is a mistake, not a comment
    for (i, t) in toks.iter().enumerate() {
        if t.kind == Kind::Comment && t.text.starts_with(DIRECTIVE) {
            found.any_directive = true;
            if !claimed[i] {
                found
                    .errs
                    .push(format!("{}: {DIRECTIVE} documents no method", pos(t.line)));
            }
        }
    }
    Ok(())
}
