//! Port of `platform/layering` (see docs/rust-port.md): the dependency rules between the concepts of the platform
//! (CLAUDE.md, design rule 1; ADR 0061, ADR 0125), checked over the Rust workspace by the tests of this crate
//! (`tests/*.rs`).
//!
//! The Go package checks the Go packages (`go list`); here a Go package is the crate in the same directory, its
//! imports are the path dependencies of the crate (`cargo metadata`), and the rules on names read the `.rs` sources of
//! a crate with the comments and the test code (`#[cfg(test)]` items and the modules they declare, `tests/`) left out.
//! This library holds the readers the tests share.

use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::path::{Path, PathBuf};
use std::process::Command;

use serde_json::Value;

/// The modules of the repository (ADR 0125 §1), one directory each at its root.
pub const MODULES: &[&str] = &[
    "contracts",
    "lib",
    "graph",
    "engine",
    "mcp",
    "gateway",
    "platform",
];

/// The components a module may depend on besides itself.
pub fn depends_on(module: &str) -> &'static [&'static str] {
    match module {
        "contracts" => &[],
        "lib" => &["contracts"],
        "graph" | "engine" | "mcp" | "gateway" => &["contracts", "lib"],
        "platform" => &["contracts", "lib", "graph", "engine", "mcp", "gateway"],
        _ => &[],
    }
}

/// The module of a package (a directory relative to the root): the first element of its path, "" for none.
pub fn component_of(pkg: &str) -> &str {
    let c = pkg.split('/').next().unwrap_or("");
    if MODULES.contains(&c) { c } else { "" }
}

/// The root of the repository.
pub fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("..").join("..")
}

/// The kind of a dependency, as `cargo metadata` names it.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum DepKind {
    Normal,
    Dev,
    Build,
}

/// A path dependency of a crate on another crate of the workspace.
#[derive(Debug, Clone)]
pub struct Dep {
    /// The directory of the crate depended on, relative to the root (its Go package).
    pub pkg: String,
    pub kind: DepKind,
}

/// A crate of the workspace.
#[derive(Debug, Clone)]
pub struct Crate {
    pub name: String,
    /// Its directory relative to the root, `/`-separated: the Go package it ports.
    pub pkg: String,
    /// Its directory on disk.
    pub dir: PathBuf,
    pub deps: Vec<Dep>,
    /// The names of its binaries.
    pub bins: Vec<String>,
}

/// The crates of the workspace, by package.
pub struct Workspace {
    pub crates: BTreeMap<String, Crate>,
}

fn rel(root: &Path, p: &Path) -> String {
    let p = p.strip_prefix(root).unwrap_or(p);
    p.components()
        .map(|c| c.as_os_str().to_string_lossy().into_owned())
        .collect::<Vec<_>>()
        .join("/")
}

impl Workspace {
    /// Reads the workspace with `cargo metadata --no-deps`.
    pub fn load() -> Result<Workspace, String> {
        let cargo = std::env::var("CARGO").unwrap_or_else(|_| "cargo".into());
        let out = Command::new(cargo)
            .args([
                "metadata",
                "--format-version",
                "1",
                "--no-deps",
                "--offline",
            ])
            .current_dir(root())
            .output()
            .map_err(|e| format!("cargo metadata: {e}"))?;
        if !out.status.success() {
            return Err(format!(
                "cargo metadata: {}",
                String::from_utf8_lossy(&out.stderr)
            ));
        }
        let v: Value =
            serde_json::from_slice(&out.stdout).map_err(|e| format!("cargo metadata: {e}"))?;
        Self::from_metadata(&v)
    }

    /// Reads the workspace from the JSON of `cargo metadata`.
    pub fn from_metadata(v: &Value) -> Result<Workspace, String> {
        let root = PathBuf::from(
            v["workspace_root"]
                .as_str()
                .ok_or("cargo metadata: no workspace_root")?,
        );
        let mut crates = BTreeMap::new();
        for p in v["packages"]
            .as_array()
            .ok_or("cargo metadata: no packages")?
        {
            let manifest = PathBuf::from(p["manifest_path"].as_str().unwrap_or_default());
            let dir = manifest.parent().unwrap_or(&root).to_path_buf();
            let pkg = rel(&root, &dir);
            let deps = p["dependencies"]
                .as_array()
                .into_iter()
                .flatten()
                .filter_map(|d| {
                    let path = d["path"].as_str()?;
                    let kind = match d["kind"].as_str() {
                        Some("dev") => DepKind::Dev,
                        Some("build") => DepKind::Build,
                        _ => DepKind::Normal,
                    };
                    Some(Dep {
                        pkg: rel(&root, Path::new(path)),
                        kind,
                    })
                })
                .collect();
            let bins = p["targets"]
                .as_array()
                .into_iter()
                .flatten()
                .filter(|t| {
                    t["kind"]
                        .as_array()
                        .is_some_and(|k| k.iter().any(|k| k == "bin"))
                })
                .filter_map(|t| t["name"].as_str().map(str::to_string))
                .collect();
            crates.insert(
                pkg.clone(),
                Crate {
                    name: p["name"].as_str().unwrap_or_default().to_string(),
                    pkg,
                    dir,
                    deps,
                    bins,
                },
            );
        }
        Ok(Workspace { crates })
    }

    /// The packages `pkg` depends on, directly or not (itself excluded): its dependencies of the kinds `first`, then
    /// their normal and build dependencies. Go's `go list -deps` (`first` = normal, build) and `go list -test -deps`
    /// (`first` = every kind).
    pub fn deps_of(&self, pkg: &str, first: &[DepKind]) -> BTreeSet<String> {
        let mut seen = BTreeSet::new();
        let mut queue = VecDeque::new();
        if let Some(c) = self.crates.get(pkg) {
            for d in c.deps.iter().filter(|d| first.contains(&d.kind)) {
                if seen.insert(d.pkg.clone()) {
                    queue.push_back(d.pkg.clone());
                }
            }
        }
        while let Some(p) = queue.pop_front() {
            if let Some(c) = self.crates.get(&p) {
                for d in &c.deps {
                    if d.kind != DepKind::Dev && seen.insert(d.pkg.clone()) {
                        queue.push_back(d.pkg.clone());
                    }
                }
            }
        }
        seen.remove(pkg);
        seen
    }
}

/// Removes the comments of a Rust source, keeping the code and the literals (strings, raw strings, chars).
/// The second result tells, per char of the first, whether it is inside a literal.
pub fn strip_comments(src: &str) -> (String, Vec<bool>) {
    let c: Vec<char> = src.chars().collect();
    let mut out = String::with_capacity(src.len());
    let mut lit = Vec::with_capacity(c.len());
    let push = |out: &mut String, lit: &mut Vec<bool>, ch: char, l: bool| {
        out.push(ch);
        lit.push(l);
    };
    let ident = |ch: char| ch.is_alphanumeric() || ch == '_';
    let mut i = 0;
    while i < c.len() {
        let ch = c[i];
        let next = c.get(i + 1).copied();
        if ch == '/' && next == Some('/') {
            while i < c.len() && c[i] != '\n' {
                i += 1;
            }
            continue;
        }
        if ch == '/' && next == Some('*') {
            let mut depth = 0;
            while i < c.len() {
                if c[i] == '/' && c.get(i + 1) == Some(&'*') {
                    depth += 1;
                    i += 2;
                } else if c[i] == '*' && c.get(i + 1) == Some(&'/') {
                    depth -= 1;
                    i += 2;
                    if depth == 0 {
                        break;
                    }
                } else {
                    i += 1;
                }
            }
            push(&mut out, &mut lit, ' ', false);
            continue;
        }
        let prev_ident = i > 0 && ident(c[i - 1]);
        // raw strings: r"..", r#".."#, br"..", cr".."
        if !prev_ident && (ch == 'r' || ((ch == 'b' || ch == 'c') && next == Some('r'))) {
            let mut j = if ch == 'r' { i + 1 } else { i + 2 };
            let mut hashes = 0;
            while c.get(j) == Some(&'#') {
                hashes += 1;
                j += 1;
            }
            if c.get(j) == Some(&'"') {
                for &k in &c[i..=j] {
                    push(&mut out, &mut lit, k, true);
                }
                j += 1;
                while j < c.len() {
                    if c[j] == '"' && (1..=hashes).all(|h| c.get(j + h) == Some(&'#')) {
                        for &k in &c[j..=j + hashes] {
                            push(&mut out, &mut lit, k, true);
                        }
                        j += hashes + 1;
                        break;
                    }
                    push(&mut out, &mut lit, c[j], true);
                    j += 1;
                }
                i = j;
                continue;
            }
        }
        if ch == '"' {
            push(&mut out, &mut lit, ch, true);
            i += 1;
            while i < c.len() {
                let k = c[i];
                push(&mut out, &mut lit, k, true);
                i += 1;
                if k == '\\' {
                    if let Some(&e) = c.get(i) {
                        push(&mut out, &mut lit, e, true);
                        i += 1;
                    }
                } else if k == '"' {
                    break;
                }
            }
            continue;
        }
        if ch == '\'' {
            // a char literal ('a', '\n', '\u{..}'), else a lifetime or a label
            let end = if next == Some('\\') {
                (i + 3..c.len()).find(|&j| c[j] == '\'')
            } else if c.get(i + 2) == Some(&'\'') {
                Some(i + 2)
            } else {
                None
            };
            if let Some(end) = end {
                for &k in &c[i..=end] {
                    push(&mut out, &mut lit, k, true);
                }
                i = end + 1;
                continue;
            }
        }
        push(&mut out, &mut lit, ch, false);
        i += 1;
    }
    (out, lit)
}

/// A source file with its comments and test code removed.
#[derive(Debug, Clone)]
pub struct Source {
    pub path: PathBuf,
    pub code: String,
}

/// Removes the items flagged `#[cfg(test)]` from a source stripped by [`strip_comments`], and returns the names of
/// the modules such an item declares out of line (`#[cfg(test)] mod name;`).
pub fn strip_test_items(code: &str, lit: &[bool]) -> (String, Vec<String>) {
    let c: Vec<char> = code.chars().collect();
    let pat: Vec<char> = "#[cfg(test)]".chars().collect();
    let mut out = String::with_capacity(code.len());
    let mut mods = Vec::new();
    let mut i = 0;
    while i < c.len() {
        if !lit[i] && c[i..].starts_with(&pat) {
            // skip the item: up to the `;` of a declaration, or the brace closing its body
            let mut j = i + pat.len();
            let mut depth = 0usize;
            let mut brackets = 0usize;
            let start = j;
            while j < c.len() {
                if !lit[j] {
                    match c[j] {
                        '[' => brackets += 1,
                        ']' => brackets = brackets.saturating_sub(1),
                        '{' => depth += 1,
                        '}' => {
                            depth -= 1;
                            if depth == 0 {
                                j += 1;
                                break;
                            }
                        }
                        ';' if depth == 0 && brackets == 0 => {
                            let decl: String = c[start..j].iter().collect();
                            let words: Vec<&str> = decl.split_whitespace().collect();
                            if let Some(p) = words.iter().position(|w| *w == "mod")
                                && let Some(name) = words.get(p + 1)
                            {
                                mods.push(name.to_string());
                            }
                            j += 1;
                            break;
                        }
                        _ => {}
                    }
                }
                j += 1;
            }
            out.push(' ');
            i = j;
            continue;
        }
        out.push(c[i]);
        i += 1;
    }
    (out, mods)
}

/// The non-test sources of the crate in `dir`: every `.rs` file under `src/`, but for the modules declared under
/// `#[cfg(test)]` and their directories, files named `*_test.rs` and directories named `tests`; comments and
/// `#[cfg(test)]` items removed.
pub fn sources(dir: &Path) -> Result<Vec<Source>, String> {
    let src = dir.join("src");
    let mut files = Vec::new();
    collect(&src, &mut files)?;
    files.sort();
    let mut excluded: Vec<PathBuf> = Vec::new();
    let mut read = Vec::new();
    for f in &files {
        let text = std::fs::read_to_string(f).map_err(|e| format!("{}: {e}", f.display()))?;
        let (code, lit) = strip_comments(&text);
        let (code, mods) = strip_test_items(&code, &lit);
        let stem = f.file_stem().unwrap_or_default().to_string_lossy();
        let base = if matches!(stem.as_ref(), "lib" | "main" | "mod") {
            f.parent().unwrap_or(&src).to_path_buf()
        } else {
            f.with_extension("")
        };
        for m in mods {
            excluded.push(base.join(format!("{m}.rs")));
            excluded.push(base.join(&m));
        }
        read.push(Source {
            path: f.clone(),
            code,
        });
    }
    Ok(read
        .into_iter()
        .filter(|s| !excluded.iter().any(|e| s.path.starts_with(e)))
        .collect())
}

fn collect(dir: &Path, files: &mut Vec<PathBuf>) -> Result<(), String> {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return Ok(());
    };
    for e in entries {
        let e = e.map_err(|e| e.to_string())?;
        let p = e.path();
        let name = e.file_name().to_string_lossy().into_owned();
        if p.is_dir() {
            if name != "tests" {
                collect(&p, files)?;
            }
        } else if name.ends_with(".rs") && !name.ends_with("_test.rs") && name != "tests.rs" {
            files.push(p);
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strip_comments_keeps_literals() {
        let src = "let a = \"// not a comment\"; // a comment\n/* block /* nested */ */ let b = r#\"x\"y\"#; let c = '\"'; fn f<'a>() {}";
        let (code, _) = strip_comments(src);
        assert!(code.contains("\"// not a comment\""), "{code}");
        assert!(!code.contains("a comment\n"), "{code}");
        assert!(!code.contains("nested"), "{code}");
        assert!(code.contains("r#\"x\"y\"#"), "{code}");
        assert!(code.contains("'\"'"), "{code}");
        assert!(code.contains("fn f<'a>() {}"), "{code}");
    }

    #[test]
    fn strip_test_items_removes_modules() {
        let src = "fn a() {}\n#[cfg(test)]\nmod tests {\n fn b() { let s = \"}\"; }\n}\n#[cfg(test)]\nmod more_test;\nfn c() {}";
        let (code, lit) = strip_comments(src);
        let (code, mods) = strip_test_items(&code, &lit);
        assert!(code.contains("fn a()") && code.contains("fn c()"), "{code}");
        assert!(!code.contains("fn b()"), "{code}");
        assert_eq!(mods, vec!["more_test".to_string()]);
    }

    #[test]
    fn sources_leave_tests_out() {
        let files = sources(&root().join("platform/eventsvc")).unwrap();
        assert!(files.iter().any(|s| s.path.ends_with("hub.rs")));
        // `#[cfg(test)] mod hub_test;` in lib.rs
        assert!(!files.iter().any(|s| s.path.ends_with("hub_test.rs")));
        let lib = files.iter().find(|s| s.path.ends_with("lib.rs")).unwrap();
        assert!(!lib.code.contains("mod hub_test"), "{}", lib.code);
    }

    #[test]
    fn components() {
        assert_eq!(component_of("graph/graph"), "graph");
        assert_eq!(component_of("web"), "");
    }
}
