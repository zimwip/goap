//! The file operations of a workspace, confined to its directory (`files.go`).

use std::collections::{BTreeMap, BTreeSet};
use std::io;

use cap_std::ambient_authority;
use cap_std::fs::{Dir, Metadata};
use goap_connectorkit::InvokeError;
use regex::bytes::Regex;
use serde_json::{Map, Value, json};

use crate::workspace::{
    OP_EDIT, OP_GLOB, OP_LS, OP_READ, OP_WRITE, Space, bool_arg, clean, dir_of, int_arg, rel,
    string_arg, to_map,
};

// Bounds of the file operations: what a model is given back stays readable.

/// MaxFileSize bounds a file read or searched.
pub const MAX_FILE_SIZE: u64 = 4 << 20;
/// DefaultReadLines is the number of lines read when the call names none.
pub const DEFAULT_READ_LINES: i64 = 2000;
/// MaxLineLength cuts the longer lines of a read or a search.
pub const MAX_LINE_LENGTH: usize = 2000;
/// DefaultResults bounds a glob or a search.
pub const DEFAULT_RESULTS: i64 = 250;

/// skipDirs are never walked by glob and grep: the metadata of the sources.
const SKIP_DIRS: &[&str] = &[".git"];

/// An error of the file system as Go words it: `<op> <path>: <reason>`.
pub(crate) fn path_err(op: &str, p: &str, e: io::Error) -> InvokeError {
    format!("{op} {p}: {}", reason(&e)).into()
}

/// The reason of an I/O error in Go's words (`no such file or directory`, `path escapes from parent`, ...).
fn reason(e: &io::Error) -> String {
    let msg = e.to_string();
    if msg.contains("outside of the filesystem") || msg.contains("escape") {
        return "path escapes from parent".into();
    }
    match e.kind() {
        io::ErrorKind::NotFound => "no such file or directory".into(),
        io::ErrorKind::AlreadyExists => "file exists".into(),
        io::ErrorKind::NotADirectory => "not a directory".into(),
        io::ErrorKind::IsADirectory => "is a directory".into(),
        io::ErrorKind::PermissionDenied => "permission denied".into(),
        _ => match msg.find(" (os error") {
            Some(i) => msg[..i].to_lowercase(),
            None => msg,
        },
    }
}

pub(crate) fn files(
    s: &Space,
    op: &str,
    args: &Map<String, Value>,
) -> Result<Map<String, Value>, InvokeError> {
    let root = Dir::open_ambient_dir(&s.dir, ambient_authority())
        .map_err(|e| path_err("openat", &s.dir_str(), e))?;
    let p = rel(string_arg(args, "path"));
    match op {
        OP_READ => read(
            &root,
            &p,
            int_arg(args, "offset", 1),
            int_arg(args, "limit", DEFAULT_READ_LINES),
        ),
        OP_WRITE => write(&root, &p, string_arg(args, "content")),
        OP_EDIT => edit(
            &root,
            &p,
            string_arg(args, "old"),
            string_arg(args, "new"),
            bool_arg(args, "all"),
        ),
        OP_LS => ls(&root, &p),
        OP_GLOB => glob(
            &root,
            &p,
            string_arg(args, "pattern"),
            int_arg(args, "limit", DEFAULT_RESULTS),
        ),
        _ => grep(&root, &p, args),
    }
}

fn read_text(root: &Dir, p: &str) -> Result<Vec<u8>, InvokeError> {
    let info = root.metadata(p).map_err(|e| path_err("statat", p, e))?;
    if info.is_dir() {
        return Err(format!("{p} is a directory").into());
    }
    if info.len() > MAX_FILE_SIZE {
        return Err(format!("{p} is larger than {MAX_FILE_SIZE} bytes").into());
    }
    let b = root.read(p).map_err(|e| path_err("openat", p, e))?;
    if binary(&b) {
        return Err(format!("{p} is not a text file").into());
    }
    Ok(b)
}

fn binary(b: &[u8]) -> bool {
    b[..b.len().min(8000)].contains(&0)
}

/// The line cut at MaxLineLength bytes (on a character boundary), "…" marking the cut.
pub(crate) fn cut_line(l: &str) -> String {
    if l.len() <= MAX_LINE_LENGTH {
        return l.to_string();
    }
    let mut cut = MAX_LINE_LENGTH;
    while cut > 0 && !l.is_char_boundary(cut) {
        cut -= 1;
    }
    format!("{}…", &l[..cut])
}

fn read(root: &Dir, p: &str, offset: i64, limit: i64) -> Result<Map<String, Value>, InvokeError> {
    let b = read_text(root, p)?;
    let offset = offset.max(1);
    let limit = if limit < 1 { DEFAULT_READ_LINES } else { limit };
    let text = String::from_utf8_lossy(&b);
    let mut lines: Vec<&str> = text.split('\n').collect();
    if lines.last() == Some(&"") {
        lines.pop();
    }
    let mut out = String::new();
    let last = (lines.len() as i64).min(offset - 1 + limit);
    let mut i = offset - 1;
    while i < last {
        out.push_str(&format!("{:6}\t{}\n", i + 1, cut_line(lines[i as usize])));
        i += 1;
    }
    Ok(to_map(
        json!({"path": p, "text": out, "lines": lines.len() as f64, "from": offset as f64,
        "to": last as f64, "truncated": last < lines.len() as i64}),
    ))
}

fn write(root: &Dir, p: &str, content: &str) -> Result<Map<String, Value>, InvokeError> {
    if p == "." {
        return Err("write needs a file path".into());
    }
    let created = matches!(root.metadata(p), Err(e) if e.kind() == io::ErrorKind::NotFound);
    let dir = dir_of(p);
    if dir != "." {
        root.create_dir_all(&dir)
            .map_err(|e| path_err("mkdirat", &dir, e))?;
    }
    root.write(p, content.as_bytes())
        .map_err(|e| path_err("openat", p, e))?;
    Ok(to_map(
        json!({"path": p, "bytes": content.len() as f64, "created": created}),
    ))
}

fn edit(
    root: &Dir,
    p: &str,
    old: &str,
    repl: &str,
    all: bool,
) -> Result<Map<String, Value>, InvokeError> {
    if old.is_empty() {
        return Err("old must not be empty: write creates a file".into());
    }
    if old == repl {
        return Err("old and new are the same".into());
    }
    let b = read_text(root, p)?;
    let text = String::from_utf8_lossy(&b).into_owned();
    let n = text.matches(old).count();
    if n == 0 {
        return Err(format!("old was not found in {p}").into());
    }
    if n > 1 && !all {
        return Err(format!(
            "old was found {n} times in {p}: give more context to make it unique, or all"
        )
        .into());
    }
    let text = if all {
        text.replace(old, repl)
    } else {
        text.replacen(old, repl, 1)
    };
    root.write(p, text.as_bytes())
        .map_err(|e| path_err("openat", p, e))?;
    Ok(to_map(json!({"path": p, "replacements": n as f64})))
}

/// The entries of a directory of the root, sorted by name, with their metadata (not following links).
fn read_dir(root: &Dir, p: &str) -> io::Result<Vec<(String, Metadata)>> {
    let mut out = Vec::new();
    for e in root.read_dir(p)? {
        let e = e?;
        let name = e.file_name().to_string_lossy().into_owned();
        let md = e.metadata()?;
        out.push((name, md));
    }
    out.sort_by(|a, b| a.0.cmp(&b.0));
    Ok(out)
}

fn ls(root: &Dir, p: &str) -> Result<Map<String, Value>, InvokeError> {
    let entries = read_dir(root, p).map_err(|e| path_err("open", p, e))?;
    let out: Vec<Value> = entries
        .into_iter()
        .map(|(name, md)| {
            let dir = md.is_dir();
            let mut item = to_map(json!({"name": name, "dir": dir}));
            if !dir {
                item.insert("size".into(), json!(md.len() as f64));
            }
            Value::Object(item)
        })
        .collect();
    Ok(to_map(json!({"path": p, "entries": out})))
}

/// Go's `path.Match` of one element (no '/'): `*`, `?`, `[...]` classes (ranges, `^` negation) and `\` escapes.
/// Err: the pattern is malformed (`ErrBadPattern`).
pub(crate) fn path_match(pattern: &str, name: &str) -> Result<bool, ()> {
    let p: Vec<char> = pattern.chars().collect();
    let n: Vec<char> = name.chars().collect();
    match_from(&p, &n)
}

fn match_from(p: &[char], n: &[char]) -> Result<bool, ()> {
    let mut pi = 0;
    let mut ni = 0;
    // the position to come back to after a star
    let mut star: Option<(usize, usize)> = None;
    loop {
        if pi < p.len() {
            match p[pi] {
                '*' => {
                    // collapse stars
                    while pi < p.len() && p[pi] == '*' {
                        pi += 1;
                    }
                    star = Some((pi, ni));
                    continue;
                }
                c => {
                    if ni < n.len() && n[ni] != '/' {
                        let (ok, next) = match_one(p, pi, n[ni])?;
                        if ok {
                            pi = next;
                            ni += 1;
                            continue;
                        }
                    } else if c == '[' || c == '\\' {
                        // check the pattern is well formed even when the name is spent
                        match_one(p, pi, '\0')?;
                    }
                }
            }
        } else if ni == n.len() {
            return Ok(true);
        }
        // mismatch: backtrack to the last star, consuming one more character of the name
        match star {
            Some((sp, sn)) if sn < n.len() && n[sn] != '/' => {
                star = Some((sp, sn + 1));
                pi = sp;
                ni = sn + 1;
            }
            _ => {
                // the rest of the pattern must still be well formed
                check_pattern(&p[pi.min(p.len())..])?;
                return Ok(false);
            }
        }
    }
}

fn check_pattern(p: &[char]) -> Result<(), ()> {
    let mut i = 0;
    while i < p.len() {
        match p[i] {
            '*' => i += 1,
            _ => {
                let (_, next) = match_one(p, i, '\0')?;
                i = next;
            }
        }
    }
    Ok(())
}

/// Matches one pattern item at p[i] against c: whether it matched and the index after the item.
fn match_one(p: &[char], i: usize, c: char) -> Result<(bool, usize), ()> {
    match p[i] {
        '?' => Ok((c != '/', i + 1)),
        '\\' => {
            let Some(&e) = p.get(i + 1) else {
                return Err(());
            };
            Ok((c == e, i + 2))
        }
        '[' => {
            let mut j = i + 1;
            let negated = p.get(j) == Some(&'^');
            if negated {
                j += 1;
            }
            let mut matched = false;
            let mut first = true;
            loop {
                let Some(&ch) = p.get(j) else {
                    return Err(());
                };
                if ch == ']' && !first {
                    j += 1;
                    break;
                }
                first = false;
                let (lo, nj) = class_char(p, j)?;
                j = nj;
                let mut hi = lo;
                if p.get(j) == Some(&'-') {
                    let (h, nj) = class_char(p, j + 1)?;
                    hi = h;
                    j = nj;
                }
                if lo <= c && c <= hi {
                    matched = true;
                }
            }
            Ok((matched != negated && c != '/', j))
        }
        ch => Ok((c == ch, i + 1)),
    }
}

fn class_char(p: &[char], j: usize) -> Result<(char, usize), ()> {
    match p.get(j) {
        None | Some('-') | Some(']') => Err(()),
        Some('\\') => p.get(j + 1).map(|&c| (c, j + 2)).ok_or(()),
        Some(&c) => Ok((c, j + 1)),
    }
}

/// match reports whether a slash-separated path matches a glob pattern where ** stands for any number of
/// directories.
pub(crate) fn match_glob(pattern: &str, name: &str) -> bool {
    let pat: Vec<&str> = pattern.split('/').collect();
    let name: Vec<&str> = name.split('/').collect();
    match_parts(&pat, &name)
}

fn match_parts(mut pat: &[&str], mut name: &[&str]) -> bool {
    while let Some((first, rest)) = pat.split_first() {
        if *first == "**" {
            return (0..=name.len()).any(|i| match_parts(rest, &name[i..]));
        }
        let Some((n, nrest)) = name.split_first() else {
            return false;
        };
        if !matches!(path_match(first, n), Ok(true)) {
            return false;
        }
        pat = rest;
        name = nrest;
    }
    name.is_empty()
}

/// walk visits the files under p, sources metadata left out, with their path and their path relative to p.
/// The visitor returns false to stop the walk.
fn walk(
    root: &Dir,
    p: &str,
    visit: &mut dyn FnMut(&str, &str, &str, &Metadata) -> bool,
) -> Result<(), InvokeError> {
    let md = root
        .symlink_metadata(p)
        .map_err(|e| path_err("lstat", p, e))?;
    let name = p.rsplit('/').next().unwrap_or(p).to_string();
    if !md.is_dir() {
        let relative = relative_to(p, p);
        visit(p, &relative, &name, &md);
        return Ok(());
    }
    walk_dir(root, p, p, visit).map(|_| ())
}

fn relative_to(p: &str, full: &str) -> String {
    if p == "." {
        return full.to_string();
    }
    full.strip_prefix(&format!("{p}/"))
        .unwrap_or(full)
        .to_string()
}

/// Walks the directory full: false when the visitor stopped the walk.
fn walk_dir(
    root: &Dir,
    p: &str,
    full: &str,
    visit: &mut dyn FnMut(&str, &str, &str, &Metadata) -> bool,
) -> Result<bool, InvokeError> {
    let entries = read_dir(root, full).map_err(|e| path_err("open", full, e))?;
    for (name, md) in entries {
        let child = if full == "." {
            name.clone()
        } else {
            format!("{full}/{name}")
        };
        if md.is_dir() {
            if SKIP_DIRS.contains(&name.as_str()) {
                continue;
            }
            if !walk_dir(root, p, &child, visit)? {
                return Ok(false);
            }
            continue;
        }
        let relative = relative_to(p, &child);
        if !visit(&child, &relative, &name, &md) {
            return Ok(false);
        }
    }
    Ok(true)
}

fn glob(root: &Dir, p: &str, pattern: &str, limit: i64) -> Result<Map<String, Value>, InvokeError> {
    if pattern.is_empty() {
        return Err("glob needs a pattern".into());
    }
    let cleaned = clean(pattern);
    let pattern = cleaned.strip_prefix('/').unwrap_or(&cleaned).to_string();
    let limit = if limit < 1 { DEFAULT_RESULTS } else { limit } as usize;
    let mut hits: Vec<(String, i128)> = Vec::new();
    walk(root, p, &mut |full, relative, _name, md| {
        if match_glob(&pattern, relative) {
            let modified = md
                .modified()
                .ok()
                .and_then(|t| t.into_std().duration_since(std::time::UNIX_EPOCH).ok())
                .map_or(0, |d| d.as_nanos() as i128);
            hits.push((full.to_string(), modified));
        }
        true
    })?;
    hits.sort_by_key(|h| std::cmp::Reverse(h.1));
    let truncated = hits.len() > limit;
    hits.truncate(limit);
    let out: Vec<Value> = hits.into_iter().map(|(p, _)| Value::String(p)).collect();
    Ok(to_map(json!({"files": out, "truncated": truncated})))
}

fn grep(root: &Dir, p: &str, args: &Map<String, Value>) -> Result<Map<String, Value>, InvokeError> {
    let mut expr = string_arg(args, "pattern").to_string();
    if bool_arg(args, "ignoreCase") {
        expr = format!("(?i){expr}");
    }
    let re = Regex::new(&expr).map_err(|e| -> InvokeError { format!("pattern: {e}").into() })?;
    let mode = match string_arg(args, "mode") {
        "" => "files",
        m => m,
    };
    if !matches!(mode, "files" | "content" | "count") {
        return Err(format!("mode {mode:?}: files, content or count").into());
    }
    let only = string_arg(args, "glob");
    let around = int_arg(args, "context", 0).max(0) as usize;
    let limit = int_arg(args, "limit", DEFAULT_RESULTS);
    let limit = if limit < 1 { DEFAULT_RESULTS } else { limit } as usize;
    let mut results: Vec<Value> = Vec::new();
    let mut truncated = false;
    walk(root, p, &mut |full, relative, name, md| {
        if !only.is_empty() {
            let target = if only.contains('/') { relative } else { name };
            if !match_glob(only, target) {
                return true;
            }
        }
        if md.len() > MAX_FILE_SIZE {
            return true;
        }
        let Ok(b) = root.read(full) else {
            return true;
        };
        if binary(&b) || !re.is_match(&b) {
            return true;
        }
        if results.len() >= limit {
            truncated = true;
            return false;
        }
        match mode {
            "files" => results.push(Value::String(full.to_string())),
            "count" => {
                results.push(json!({"path": full, "count": re.find_iter(&b).count() as f64}))
            }
            _ => results.push(json!({"path": full, "lines": matching_lines(&b, &re, around)})),
        }
        true
    })?;
    Ok(to_map(
        json!({"mode": mode, "results": results, "truncated": truncated}),
    ))
}

/// matchingLines are the lines of a file matching re, "<n>:<text>", with the lines around them "<n>-<text>".
fn matching_lines(b: &[u8], re: &Regex, around: usize) -> String {
    // bufio.ScanLines: lines split on \n, a final \r dropped, no empty last line
    let mut lines: Vec<&[u8]> = b
        .split(|c| *c == b'\n')
        .map(|l| l.strip_suffix(b"\r").unwrap_or(l))
        .collect();
    if b.ends_with(b"\n") || b.is_empty() {
        lines.pop();
    }
    let mut keep = BTreeSet::new();
    let mut hit = BTreeMap::new();
    for (i, l) in lines.iter().enumerate() {
        if re.is_match(l) {
            hit.insert(i, true);
            let lo = i.saturating_sub(around);
            let hi = (i + around).min(lines.len().saturating_sub(1));
            keep.extend(lo..=hi);
        }
    }
    let mut out = String::new();
    let mut prev: Option<usize> = None;
    for (i, l) in lines.iter().enumerate() {
        if !keep.contains(&i) {
            continue;
        }
        if around > 0
            && let Some(prev) = prev
            && i > prev + 1
        {
            out.push_str("--\n");
        }
        let sep = if hit.contains_key(&i) { ":" } else { "-" };
        out.push_str(&format!(
            "{}{sep}{}\n",
            i + 1,
            cut_line(&String::from_utf8_lossy(l))
        ));
        prev = Some(i);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn path_match_as_go() {
        for (p, n, want) in [
            ("*.go", "main.go", Ok(true)),
            ("*.go", "main.rs", Ok(false)),
            ("a?c", "abc", Ok(true)),
            ("[a-c]x", "bx", Ok(true)),
            ("[^a-c]x", "bx", Ok(false)),
            ("\\*", "*", Ok(true)),
            ("*", "", Ok(true)),
            ("a*b*c", "axxbyyc", Ok(true)),
            ("a*b*c", "axxbyy", Ok(false)),
            ("[", "a", Err(())),
            ("[a-", "a", Err(())),
        ] {
            assert_eq!(path_match(p, n), want, "{p} {n}");
        }
    }

    #[test]
    fn lines_cut_on_a_character() {
        let l = "é".repeat(MAX_LINE_LENGTH);
        let got = cut_line(&l);
        assert!(got.ends_with('…') && got.len() <= MAX_LINE_LENGTH + "…".len());
    }
}
