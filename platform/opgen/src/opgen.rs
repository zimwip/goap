//! The scan, the checks and the renderers (`opgen.go`).
use std::{
    collections::{BTreeMap, HashMap},
    fs,
    path::{Path, PathBuf},
    sync::LazyLock,
};

use regex::Regex;

use crate::{golang, rust};

/// The comment that declares an operation on the Go method performing it.
pub const DIRECTIVE: &str = "//goap:operation";

/// The doc line (`/// goap:operation <code> <kind>`) that declares an operation on the Rust method performing it.
pub const RUST_DIRECTIVE: &str = "goap:operation";

/// The type whose methods carry the annotations.
pub const RECEIVER: &str = "Graph";

static CODE_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[a-z][a-z0-9]*(\.[a-z][a-z0-9-]*)+$").unwrap());

pub(crate) fn valid_code(code: &str) -> bool {
    CODE_RE.is_match(code)
}

pub(crate) fn valid_kind(kind: &str) -> bool {
    kind == "doer" || kind == "reviewer"
}

/// The language of the source an annotation was found in.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Source {
    Go,
    Rust,
}

/// An annotation found in the sources.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Operation {
    pub code: String,
    pub kind: String,
    /// `Graph.<name>`.
    pub method: String,
    /// The first sentence of the method's documentation.
    pub doc: String,
    /// `file:line` of the annotation.
    pub pos: String,
    /// The language of the annotated method.
    pub source: Source,
}

/// The errors of the generator.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("{0}: {1}")]
    Io(String, #[source] std::io::Error),
    #[error("{0}")]
    Parse(String),
    /// Every problem found in the annotations, one per line (Go's `errors.Join`).
    #[error("{}", .0.join("\n"))]
    Problems(Vec<String>),
}

/// The name of the Go constant of a code: "impact.link-create" → "ImpactLinkCreate".
pub fn go_const(code: &str) -> String {
    code.split(['.', '-'])
        .filter(|p| !p.is_empty())
        .map(|p| {
            let mut c = p.chars();
            match c.next() {
                Some(f) => f.to_uppercase().chain(c).collect::<String>(),
                None => String::new(),
            }
        })
        .collect()
}

/// The name of the Rust constant of a code: "impact.link-create" → "IMPACT_LINK_CREATE".
pub fn rust_const(code: &str) -> String {
    code.split(['.', '-'])
        .filter(|p| !p.is_empty())
        .map(str::to_uppercase)
        .collect::<Vec<_>>()
        .join("_")
}

/// The constant of a code in the language of a source.
fn const_of(source: Source, code: &str) -> String {
    match source {
        Source::Go => go_const(code),
        Source::Rust => rust_const(code),
    }
}

/// What a scanner of one language found in a directory.
#[derive(Default)]
pub(crate) struct Found {
    pub ops: Vec<Operation>,
    /// The problems found while reading the annotations, in source order.
    pub errs: Vec<String>,
    /// constant → the positions of the calls of `allow` naming it.
    pub allows: BTreeMap<String, Vec<String>>,
    /// code → the constants the `allow` calls of its method's body name.
    pub bodies: HashMap<String, Vec<String>>,
    /// Whether the sources hold any directive at all.
    pub any_directive: bool,
}

/// Reads the sources of dir and returns the operations they declare, sorted by code, with every problem found
/// (joined).
///
/// While the graph is ported, the sources are the Rust files under `dir/src` (recursively, `#[cfg(test)]` items left
/// out) when they hold any `/// goap:operation` annotation, else the non-test Go files of dir.
pub fn scan(dir: impl AsRef<Path>) -> Result<Vec<Operation>, Error> {
    let dir = dir.as_ref();
    let rs = rust::scan(&dir.join("src"))?;
    let (found, source) = if rs.any_directive {
        (rs, Source::Rust)
    } else {
        (golang::scan(dir)?, Source::Go)
    };
    check(found, source)
}

fn check(found: Found, source: Source) -> Result<Vec<Operation>, Error> {
    let Found {
        mut ops,
        mut errs,
        allows,
        bodies,
        ..
    } = found;
    ops.sort_by(|a, b| a.code.cmp(&b.code));
    let (allow_call, sep) = match source {
        Source::Go => ("g.allow(ctx, id, operations.", "."),
        Source::Rust => ("self.allow(ctx, id, operations::", "::"),
    };
    let directive = match source {
        Source::Go => DIRECTIVE.to_string(),
        Source::Rust => format!("/// {RUST_DIRECTIVE}"),
    };
    let mut codes: HashMap<&str, &Operation> = HashMap::new();
    let mut consts: HashMap<String, &Operation> = HashMap::new();
    for o in &ops {
        if let Some(prev) = codes.get(o.code.as_str()) {
            errs.push(format!(
                "{}: operation {} declared twice (also on {}, {})",
                o.pos, o.code, prev.method, prev.pos
            ));
            continue;
        }
        codes.insert(&o.code, o);
        let c = const_of(source, &o.code);
        if let Some(prev) = consts.get(&c) {
            errs.push(format!(
                "{}: operations {} and {} give the same constant {}",
                o.pos, prev.code, o.code, c
            ));
            continue;
        }
        consts.insert(c.clone(), o);
        let asks = bodies.get(&o.code).is_some_and(|b| b.contains(&c));
        if !asks {
            errs.push(format!(
                "{}: {} performs {} but never asks for it: call {}{}) first",
                o.pos, o.method, o.code, allow_call, c
            ));
        }
    }
    for (name, positions) in &allows {
        if !consts.contains_key(name) {
            let call = allow_call.split('(').next().unwrap_or_default();
            for pos in positions {
                errs.push(format!(
                    "{pos}: {call} names operations{sep}{name}, which no {directive} annotation declares"
                ));
            }
        }
    }
    if errs.is_empty() {
        Ok(ops)
    } else {
        Err(Error::Problems(errs))
    }
}

/// Returns the Go source of the catalogue of package pkg, formatted as gofmt does.
pub fn render_go(pkg: &str, ops: &[Operation]) -> String {
    let mut b = String::new();
    b.push_str(&format!(
        "// Code generated by goap-opgen from the {DIRECTIVE} annotations of graph/graph. DO NOT EDIT.\n\n"
    ));
    b.push_str(&format!("package {pkg}\n\n"));
    b.push_str("// The operations of the catalogue.\nconst (\n");
    let rows: Vec<(String, String, String)> = ops
        .iter()
        .map(|o| {
            (
                go_const(&o.code),
                go_quote(&o.code),
                format!("{}, {}", o.method, o.kind),
            )
        })
        .collect();
    let wn = rows.iter().map(|r| r.0.chars().count()).max().unwrap_or(0);
    let wv = rows.iter().map(|r| r.1.chars().count()).max().unwrap_or(0);
    for (n, v, c) in &rows {
        b.push_str(&format!("\t{n:<wn$} = {v:<wv$} // {c}\n"));
    }
    b.push_str(")\n\nvar catalog = []Operation{\n");
    for o in ops {
        let k = if o.kind == "reviewer" {
            "Reviewer"
        } else {
            "Doer"
        };
        b.push_str(&format!(
            "\t{{Code: {}, Kind: {}, Method: {}, Doc: {}}},\n",
            go_const(&o.code),
            k,
            go_quote(&o.method),
            go_quote(&o.doc)
        ));
    }
    b.push_str("}\n");
    b
}

/// Returns the Rust source of the catalogue (`contracts/operations/src/operations_gen.rs`), formatted as rustfmt
/// does.
pub fn render_rust(ops: &[Operation]) -> String {
    let mut b = String::new();
    b.push_str(&format!(
        "// Code generated by goap-opgen (platform/opgen) from the {RUST_DIRECTIVE} annotations of graph/graph. DO NOT EDIT.\n\n"
    ));
    b.push_str("//! The operations of the catalogue.\nuse crate::{Kind, Operation};\n");
    for o in ops {
        b.push_str(&format!(
            "\n/// {}, {}.\npub const {}: &str = {};\n",
            o.method,
            o.kind,
            rust_const(&o.code),
            rust_quote(&o.code)
        ));
    }
    b.push_str("\npub(crate) static CATALOG: &[Operation] = &[\n");
    for o in ops {
        let k = if o.kind == "reviewer" {
            "Reviewer"
        } else {
            "Doer"
        };
        b.push_str(&format!(
            "    Operation {{\n        code: {},\n        kind: Kind::{},\n        method: {},\n        doc: {},\n    }},\n",
            rust_const(&o.code),
            k,
            rust_quote(&o.method),
            rust_quote(&o.doc)
        ));
    }
    b.push_str("];\n");
    b
}

/// Scans src and writes the catalogue to out, unless it is already current: Rust when out ends in `.rs`, else Go
/// (package of out's directory name).
pub fn generate(src: impl AsRef<Path>, out: impl AsRef<Path>) -> Result<(), Error> {
    let out = out.as_ref();
    let ops = scan(src)?;
    let code = if out.extension().is_some_and(|e| e == "rs") {
        render_rust(&ops)
    } else {
        let abs: PathBuf = if out.is_absolute() {
            out.to_path_buf()
        } else {
            std::env::current_dir()
                .map_err(|e| Error::Io(out.display().to_string(), e))?
                .join(out)
        };
        let pkg = abs
            .parent()
            .and_then(|p| p.file_name())
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_default();
        render_go(&pkg, &ops)
    };
    if fs::read(out).is_ok_and(|cur| cur == code.as_bytes()) {
        return Ok(());
    }
    fs::write(out, code).map_err(|e| Error::Io(out.display().to_string(), e))
}

/// The first sentence of a doc comment, its whitespace collapsed.
pub(crate) fn first_sentence(doc: &str) -> String {
    let mut text = doc.split_whitespace().collect::<Vec<_>>().join(" ");
    if let Some(i) = text.find(". ") {
        text.truncate(i + 1);
    }
    text
}

/// Go's `strconv.Quote` (`%q`).
pub fn go_quote(s: &str) -> String {
    let mut b = String::with_capacity(s.len() + 2);
    b.push('"');
    for c in s.chars() {
        match c {
            '"' => b.push_str("\\\""),
            '\\' => b.push_str("\\\\"),
            '\u{7}' => b.push_str("\\a"),
            '\u{8}' => b.push_str("\\b"),
            '\u{c}' => b.push_str("\\f"),
            '\n' => b.push_str("\\n"),
            '\r' => b.push_str("\\r"),
            '\t' => b.push_str("\\t"),
            '\u{b}' => b.push_str("\\v"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => b.push_str(&format!("\\x{:02x}", c as u32)),
            c if c.is_control() => {
                if (c as u32) < 0x10000 {
                    b.push_str(&format!("\\u{:04x}", c as u32))
                } else {
                    b.push_str(&format!("\\U{:08x}", c as u32))
                }
            }
            c => b.push(c),
        }
    }
    b.push('"');
    b
}

/// A Rust string literal of s.
pub fn rust_quote(s: &str) -> String {
    format!("{s:?}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    fn root() -> PathBuf {
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
    }

    /// The catalogue in contracts/operations is the one the annotations of graph/graph give: run goap-opgen after
    /// adding, renaming or removing an annotation.
    #[test]
    fn catalogue_is_current() {
        let ops = scan(root().join("graph/graph")).unwrap();
        let want = render_rust(&ops);
        let path = root().join("contracts/operations/src/operations_gen.rs");
        if std::env::var("GOAP_UPDATE_OPERATIONS").is_ok_and(|v| v == "1") {
            fs::write(&path, &want).unwrap();
        }
        let got = fs::read_to_string(&path).unwrap();
        assert!(
            got == want,
            "contracts/operations/src/operations_gen.rs is out of date: run goap-opgen \
             -src graph/graph -out contracts/operations/src/operations_gen.rs"
        );
        // while the Go catalogue is there, the Go renderer gives it byte for byte
        let go = root().join("contracts/operations/operations_gen.go");
        if ops.iter().all(|o| o.source == Source::Go)
            && let Ok(cur) = fs::read_to_string(go)
        {
            assert_eq!(cur, render_go("operations", &ops));
        }
    }

    static N: AtomicUsize = AtomicUsize::new(0);

    fn temp_dir() -> PathBuf {
        let d = std::env::temp_dir().join(format!(
            "goap-opgen-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn scan_source(src: &str) -> Result<Vec<Operation>, Error> {
        let dir = temp_dir();
        fs::write(dir.join("g.go"), format!("package graph\n\n{src}")).unwrap();
        let r = scan(&dir);
        let _ = fs::remove_dir_all(&dir);
        r
    }

    fn scan_rust(src: &str) -> Result<Vec<Operation>, Error> {
        let dir = temp_dir();
        fs::create_dir_all(dir.join("src")).unwrap();
        fs::write(dir.join("src/g.rs"), src).unwrap();
        let r = scan(&dir);
        let _ = fs::remove_dir_all(&dir);
        r
    }

    const HEADER: &str = r#"
import "context"

type Graph struct{}

var operations struct{ ImpactReview, ImpactUpdate string }

func (g *Graph) allow(ctx context.Context, id string, op string) error { return nil }
"#;

    #[test]
    fn scan_go() {
        let ops = scan_source(&format!(
            "{HEADER}{}",
            r#"
// Review judges an impact.
//
//goap:operation impact.review reviewer
func (g *Graph) Review(ctx context.Context, id string) error {
	if err := g.allow(ctx, id, operations.ImpactReview); err != nil {
		return err
	}
	return nil
}

// Batch reviews several: the same operation, not declared again.
func (g *Graph) Batch(ctx context.Context, id string) error {
	return g.allow(ctx, id, operations.ImpactReview)
}
"#
        ))
        .unwrap();
        assert_eq!(ops.len(), 1, "ops = {ops:?}");
        let o = &ops[0];
        assert!(
            o.code == "impact.review"
                && o.kind == "reviewer"
                && o.method == "Graph.Review"
                && o.doc == "Review judges an impact.",
            "ops = {ops:?}"
        );
        assert_eq!(go_const("impact.link-create"), "ImpactLinkCreate");
        assert_eq!(go_const("change.merge"), "ChangeMerge");
        assert_eq!(rust_const("impact.link-create"), "IMPACT_LINK_CREATE");
    }

    #[test]
    fn scan_refuses() {
        let method = |name: &str, directive: &str, body: &str| {
            format!(
                "\n// {name} does it.\n//\n{directive}\nfunc (g *Graph) {name}(ctx context.Context, id string) error {{\n{body}\n\treturn nil\n}}\n"
            )
        };
        let asks = "\tg.allow(ctx, id, operations.ImpactReview)";
        let cases: Vec<(&str, String, &str)> = vec![
            (
                "duplicate code",
                method("A", "//goap:operation impact.review reviewer", asks)
                    + &method("B", "//goap:operation impact.review reviewer", asks),
                "operation impact.review declared twice",
            ),
            (
                "same constant",
                method(
                    "A",
                    "//goap:operation impact.link.create doer",
                    "\tg.allow(ctx, id, operations.ImpactLinkCreate)",
                ) + &method(
                    "B",
                    "//goap:operation impact.link-create doer",
                    "\tg.allow(ctx, id, operations.ImpactLinkCreate)",
                ),
                "give the same constant ImpactLinkCreate",
            ),
            (
                "unknown kind",
                method("A", "//goap:operation impact.review judge", asks),
                r#"unknown kind "judge""#,
            ),
            (
                "bad code",
                method("A", "//goap:operation Review reviewer", asks),
                r#"operation code "Review""#,
            ),
            (
                "missing kind",
                method("A", "//goap:operation impact.review", asks),
                "want //goap:operation <code> <kind>",
            ),
            (
                "never asks",
                method("A", "//goap:operation impact.update doer", ""),
                "never asks for it",
            ),
            (
                "wrong ask",
                method("A", "//goap:operation impact.update doer", asks),
                "operations.ImpactReview, which no //goap:operation annotation declares",
            ),
            (
                "not a method",
                "\n// F does it.\n//\n//goap:operation impact.review reviewer\nfunc F() {}\n"
                    .into(),
                "only an exported method of *Graph",
            ),
            (
                "loose",
                "\n//goap:operation impact.review reviewer\nvar x int\n".into(),
                "documents no method",
            ),
        ];
        for (name, src, want) in cases {
            let err = scan_source(&format!("{HEADER}{src}"))
                .err()
                .unwrap_or_else(|| panic!("{name}: want an error"));
            assert!(
                err.to_string().contains(want),
                "{name}: error {err:?}, want {want:?}"
            );
        }
    }

    const RUST_HEADER: &str = r#"
pub struct Graph;

impl Graph {
    fn allow(&self, ctx: &str, id: &str, op: &str) -> Result<(), ()> { Ok(()) }
}
"#;

    #[test]
    fn scan_rust_sources() {
        let ops = scan_rust(&format!(
            "{RUST_HEADER}{}",
            r#"
impl Graph {
    /// Review judges an impact. It says so.
    ///
    /// goap:operation impact.review reviewer
    pub fn review(&self, ctx: &str, id: &str) -> Result<(), ()> {
        self.allow(ctx, id, operations::IMPACT_REVIEW)?;
        Ok(())
    }

    /// Batch reviews several: the same operation, not declared again.
    pub fn batch(&self, ctx: &str, id: &str) -> Result<(), ()> {
        self.allow(ctx, id, goap_operations::operations::IMPACT_REVIEW)
    }
}

#[cfg(test)]
mod tests {
    /// goap:operation impact.nope doer
    fn ignored() {}
}
"#
        ))
        .unwrap();
        assert_eq!(ops.len(), 1, "ops = {ops:?}");
        let o = &ops[0];
        assert!(
            o.code == "impact.review"
                && o.kind == "reviewer"
                && o.method == "Graph.review"
                && o.doc == "Review judges an impact."
                && o.source == Source::Rust
                && o.pos == "g.rs:11",
            "ops = {ops:?}"
        );
        let out = render_rust(&ops);
        assert!(out.contains("pub const IMPACT_REVIEW: &str = \"impact.review\";"));
        assert!(out.contains("kind: Kind::Reviewer,"));
    }

    #[test]
    fn scan_rust_refuses() {
        let method = |name: &str, directive: &str, body: &str| {
            format!(
                "\nimpl Graph {{\n    /// {name} does it.\n    ///\n    /// {directive}\n    pub fn {name}(&self, ctx: &str, id: &str) -> Result<(), ()> {{\n{body}\n        Ok(())\n    }}\n}}\n"
            )
        };
        let asks = "        self.allow(ctx, id, operations::IMPACT_REVIEW)?;";
        let cases: Vec<(&str, String, &str)> = vec![
            (
                "duplicate code",
                method("a", "goap:operation impact.review reviewer", asks)
                    + &method("b", "goap:operation impact.review reviewer", asks),
                "operation impact.review declared twice",
            ),
            (
                "same constant",
                method(
                    "a",
                    "goap:operation impact.link.create doer",
                    "        self.allow(ctx, id, operations::IMPACT_LINK_CREATE)?;",
                ) + &method(
                    "b",
                    "goap:operation impact.link-create doer",
                    "        self.allow(ctx, id, operations::IMPACT_LINK_CREATE)?;",
                ),
                "give the same constant IMPACT_LINK_CREATE",
            ),
            (
                "unknown kind",
                method("a", "goap:operation impact.review judge", asks),
                r#"unknown kind "judge""#,
            ),
            (
                "bad code",
                method("a", "goap:operation Review reviewer", asks),
                r#"operation code "Review""#,
            ),
            (
                "missing kind",
                method("a", "goap:operation impact.review", asks),
                "want /// goap:operation <code> <kind>",
            ),
            (
                "never asks",
                method("a", "goap:operation impact.update doer", ""),
                "never asks for it",
            ),
            (
                "wrong ask",
                method("a", "goap:operation impact.update doer", asks),
                "operations::IMPACT_REVIEW, which no /// goap:operation annotation declares",
            ),
            (
                "not a method",
                "\n/// f does it.\n///\n/// goap:operation impact.review reviewer\npub fn f() {}\n"
                    .into(),
                "only a pub method of Graph",
            ),
            (
                "private",
                "\nimpl Graph {\n    /// goap:operation impact.review reviewer\n    fn f(&self) {}\n}\n"
                    .into(),
                "only a pub method of Graph",
            ),
            (
                "loose",
                "\n/// goap:operation impact.review reviewer\npub struct X;\n".into(),
                "documents no method",
            ),
        ];
        for (name, src, want) in cases {
            let err = scan_rust(&format!("{RUST_HEADER}{src}"))
                .err()
                .unwrap_or_else(|| panic!("{name}: want an error"));
            assert!(
                err.to_string().contains(want),
                "{name}: error {err:?}, want {want:?}"
            );
        }
    }

    #[test]
    fn quotes() {
        assert_eq!(go_quote("a \"b\" §\n"), r#""a \"b\" §\n""#);
        assert_eq!(rust_quote("a \"b\" §"), r#""a \"b\" §""#);
    }
}
