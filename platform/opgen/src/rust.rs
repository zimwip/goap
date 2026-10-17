//! The Rust side of the scan: the `/// goap:operation <code> <kind>` doc lines on the `pub fn` methods of `impl
//! Graph`, read with `syn`.
use std::{
    collections::HashSet,
    fs,
    path::{Path, PathBuf},
};

use syn::{
    Attribute, Expr, ExprMethodCall, ImplItemFn, ItemFn, ItemImpl, ItemMod, TraitItemFn, Type,
    Visibility,
    spanned::Spanned,
    visit::{self, Visit},
};

use crate::opgen::{
    Error, Found, Operation, RECEIVER, RUST_DIRECTIVE, Source, first_sentence, go_quote,
    valid_code, valid_kind,
};

/// The Rust files under dir, recursively, sorted (none when dir does not exist).
fn files(dir: &Path, into: &mut Vec<PathBuf>) -> Result<(), Error> {
    let rd = match fs::read_dir(dir) {
        Ok(rd) => rd,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(Error::Io(dir.display().to_string(), e)),
    };
    for e in rd.filter_map(Result::ok) {
        let p = e.path();
        if p.is_dir() {
            files(&p, into)?;
        } else if p.extension().is_some_and(|x| x == "rs") {
            into.push(p);
        }
    }
    Ok(())
}

/// Reads the Rust files under dir.
pub(crate) fn scan(dir: &Path) -> Result<Found, Error> {
    let mut paths = Vec::new();
    files(dir, &mut paths)?;
    paths.sort();
    let mut found = Found::default();
    for path in paths {
        let name = path
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_default();
        let src =
            fs::read_to_string(&path).map_err(|e| Error::Io(path.display().to_string(), e))?;
        let file = syn::parse_file(&src).map_err(|e| {
            let at = e.span().start();
            Error::Parse(format!("{name}:{}:{}: {e}", at.line, at.column + 1))
        })?;
        let mut v = Visitor {
            file: name,
            found: &mut found,
            graph_impl: false,
            claimed: HashSet::new(),
            all: Vec::new(),
        };
        v.visit_file(&file);
        let Visitor {
            file,
            found: f,
            claimed,
            all,
            ..
        } = v;
        // a directive that documents no method is a mistake, not a comment
        for (key, line) in all {
            f.any_directive = true;
            if !claimed.contains(&key) {
                f.errs.push(format!(
                    "{file}:{line}: /// {RUST_DIRECTIVE} documents no method"
                ));
            }
        }
    }
    Ok(found)
}

/// The text of a doc attribute (`/// text` is `#[doc = " text"]`).
fn doc_of(a: &Attribute) -> Option<String> {
    if !a.path().is_ident("doc") {
        return None;
    }
    let syn::Meta::NameValue(nv) = &a.meta else {
        return None;
    };
    match &nv.value {
        Expr::Lit(syn::ExprLit {
            lit: syn::Lit::Str(s),
            ..
        }) => Some(s.value()),
        _ => None,
    }
}

/// The directive a doc line holds: the text after it.
fn directive_of(doc: &str) -> Option<&str> {
    doc.trim_start().strip_prefix(RUST_DIRECTIVE)
}

fn is_cfg_test(attrs: &[Attribute]) -> bool {
    attrs.iter().any(|a| {
        a.path().is_ident("cfg") && a.parse_args::<syn::Ident>().is_ok_and(|i| i == "test")
    })
}

fn key_of(a: &Attribute) -> (usize, usize) {
    let s = a.span().start();
    (s.line, s.column)
}

struct Visitor<'a> {
    file: String,
    found: &'a mut Found,
    graph_impl: bool,
    claimed: HashSet<(usize, usize)>,
    /// Every directive of the file, in order.
    all: Vec<((usize, usize), usize)>,
}

/// The constants the calls `<x>.allow(_, _, ..operations::<CONST>)` of a body name, with their lines.
#[derive(Default)]
struct Allows(Vec<(String, usize)>);

impl<'ast> Visit<'ast> for Allows {
    fn visit_expr_method_call(&mut self, call: &'ast ExprMethodCall) {
        if call.method == "allow"
            && call.args.len() == 3
            && let Some(Expr::Path(p)) = call.args.iter().nth(2)
        {
            let segs: Vec<_> = p.path.segments.iter().collect();
            if segs.len() >= 2 && segs[segs.len() - 2].ident == "operations" {
                self.0.push((
                    segs[segs.len() - 1].ident.to_string(),
                    call.method.span().start().line,
                ));
            }
        }
        visit::visit_expr_method_call(self, call);
    }
}

impl Visitor<'_> {
    fn allows(&mut self, block: &syn::Block) -> Vec<String> {
        let mut a = Allows::default();
        a.visit_block(block);
        let mut names = Vec::new();
        for (name, line) in a.0 {
            self.found
                .allows
                .entry(name.clone())
                .or_default()
                .push(format!("{}:{line}", self.file));
            names.push(name);
        }
        names
    }

    /// Reads the directives on a function; method is its `Graph.<name>` when it may perform an operation.
    fn directives(&mut self, attrs: &[Attribute], method: Option<String>, body: &[String]) {
        let doc: Vec<String> = attrs.iter().filter_map(doc_of).collect();
        let text: String = doc
            .iter()
            .filter(|d| directive_of(d).is_none())
            .map(|d| format!("{d}\n"))
            .collect();
        for a in attrs {
            let Some(d) = doc_of(a) else { continue };
            let Some(rest) = directive_of(&d) else {
                continue;
            };
            self.claimed.insert(key_of(a));
            let at = format!("{}:{}", self.file, a.span().start().line);
            let fields: Vec<&str> = rest.split_whitespace().collect();
            let errs = &mut self.found.errs;
            if fields.len() != 2 {
                errs.push(format!(
                    "{at}: want /// {RUST_DIRECTIVE} <code> <kind>, got {}",
                    go_quote(d.trim())
                ));
                continue;
            }
            if !valid_code(fields[0]) {
                errs.push(format!(
                    "{at}: operation code {}: lowercase dotted words, at least two (impact.review)",
                    go_quote(fields[0])
                ));
                continue;
            }
            if !valid_kind(fields[1]) {
                errs.push(format!(
                    "{at}: operation {}: unknown kind {} (doer or reviewer)",
                    fields[0],
                    go_quote(fields[1])
                ));
                continue;
            }
            let Some(method) = &method else {
                errs.push(format!(
                    "{at}: operation {}: only a pub method of {RECEIVER} performs an operation",
                    fields[0]
                ));
                continue;
            };
            self.found.ops.push(Operation {
                code: fields[0].to_string(),
                kind: fields[1].to_string(),
                method: method.clone(),
                doc: first_sentence(&text),
                pos: at,
                source: Source::Rust,
            });
            self.found
                .bodies
                .insert(fields[0].to_string(), body.to_vec());
        }
    }
}

impl<'ast> Visit<'ast> for Visitor<'_> {
    fn visit_attribute(&mut self, a: &'ast Attribute) {
        if doc_of(a).is_some_and(|d| directive_of(&d).is_some()) {
            let line = a.span().start().line;
            self.all.push((key_of(a), line));
        }
    }

    fn visit_item_mod(&mut self, m: &'ast ItemMod) {
        if !is_cfg_test(&m.attrs) {
            visit::visit_item_mod(self, m);
        }
    }

    fn visit_item_impl(&mut self, i: &'ast ItemImpl) {
        if is_cfg_test(&i.attrs) {
            return;
        }
        let graph = i.trait_.is_none()
            && matches!(&*i.self_ty, Type::Path(p) if p.qself.is_none()
                && p.path.segments.len() == 1
                && p.path.segments[0].ident == RECEIVER);
        let was = std::mem::replace(&mut self.graph_impl, graph);
        visit::visit_item_impl(self, i);
        self.graph_impl = was;
    }

    fn visit_impl_item_fn(&mut self, f: &'ast ImplItemFn) {
        if is_cfg_test(&f.attrs) {
            return;
        }
        let body = self.allows(&f.block);
        let method = (self.graph_impl
            && matches!(f.vis, Visibility::Public(_))
            && f.sig.receiver().is_some())
        .then(|| format!("{RECEIVER}.{}", f.sig.ident));
        self.directives(&f.attrs, method, &body);
        let was = std::mem::replace(&mut self.graph_impl, false);
        visit::visit_impl_item_fn(self, f);
        self.graph_impl = was;
    }

    fn visit_item_fn(&mut self, f: &'ast ItemFn) {
        if is_cfg_test(&f.attrs) {
            return;
        }
        let body = self.allows(&f.block);
        self.directives(&f.attrs, None, &body);
        let was = std::mem::replace(&mut self.graph_impl, false);
        visit::visit_item_fn(self, f);
        self.graph_impl = was;
    }

    fn visit_trait_item_fn(&mut self, f: &'ast TraitItemFn) {
        self.directives(&f.attrs, None, &[]);
        visit::visit_trait_item_fn(self, f);
    }
}
