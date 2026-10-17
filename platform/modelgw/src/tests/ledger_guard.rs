//! The gateway is the one ledger of LLM calls (ADR 0089): a call that reaches a provider without going through
//! Service::complete / Service::embed (or the RPC client to them) is invisible. This guard reads the production code
//! of the workspace and fails when a new path to a model appears: a type implementing llm::Client / llm::Embedder that
//! is neither the ledger-backed service, its client nor a wrapper of one, an llm::ClientFunc that is not the gateway's
//! complete, or a call of the router outside the service.

use std::collections::HashSet;
use std::path::{Path, PathBuf};

use syn::visit::Visit;

/// The implementations of llm::Client / llm::Embedder: the ledger-backed service, the RPC client to it, the router
/// below the ledger (only the service calls it), the instrumenting wrapper of the engine's client, and the client of
/// goap-eval, its own RPC client to the gateway until it uses this crate's Client (its sources say so).
const IMPLEMENTATIONS: &[(&str, &str)] = &[
    ("platform/modelgw", "Service"),
    ("platform/modelgw", "Client"),
    ("platform/modelgw", "Router"),
    ("engine/enginetel", "LLMClient"),
    ("platform/cmd/goap-eval", "Client"),
];

const SKIP_DIRS: &[&str] = &[
    ".claude",
    ".git",
    "gen",
    "web",
    "node_modules",
    "docs",
    "target",
    "tests",
];

#[test]
fn test_every_model_call_goes_through_the_ledger() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("..").join("..");
    let mut files = Vec::new();
    walk(&root, &mut files);
    assert!(
        files.len() > 50,
        "the workspace sources were not found under {}",
        root.display()
    );
    let mut problems = Vec::new();
    for path in files {
        let rel = path
            .strip_prefix(&root)
            .unwrap()
            .to_string_lossy()
            .replace('\\', "/");
        if rel.starts_with("contracts/llm/") {
            continue;
        }
        let src = std::fs::read_to_string(&path).unwrap();
        let file = match syn::parse_file(&src) {
            Ok(f) => f,
            Err(e) => panic!("{rel}: {e}"),
        };
        let dir = match rel.find("/src/") {
            Some(i) => rel[..i].to_string(),
            None => rel
                .rsplit_once('/')
                .map(|(d, _)| d.to_string())
                .unwrap_or_default(),
        };
        let mut v = Guard {
            rel: &rel,
            dir: &dir,
            llm_names: imported_llm_traits(&file),
            problems: &mut problems,
        };
        v.visit_file(&file);
    }
    problems.sort();
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

fn walk(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        let name = e.file_name().to_string_lossy().to_string();
        if p.is_dir() {
            if !SKIP_DIRS.contains(&name.as_str()) {
                walk(&p, out);
            }
        } else if name.ends_with(".rs") && !name.ends_with("_test.rs") && name != "tests.rs" {
            out.push(p);
        }
    }
}

/// The names the file imports llm::Client / llm::Embedder under (`use goap_llm::{Client, ...}`).
fn imported_llm_traits(f: &syn::File) -> HashSet<String> {
    fn tree(t: &syn::UseTree, from_llm: bool, out: &mut HashSet<String>) {
        match t {
            syn::UseTree::Path(p) => {
                let id = p.ident.to_string();
                tree(&p.tree, from_llm || id == "goap_llm", out);
            }
            syn::UseTree::Name(n) if from_llm => {
                let id = n.ident.to_string();
                if id == "Client" || id == "Embedder" {
                    out.insert(id);
                }
            }
            syn::UseTree::Rename(r) if from_llm => {
                let id = r.ident.to_string();
                if id == "Client" || id == "Embedder" {
                    out.insert(r.rename.to_string());
                }
            }
            syn::UseTree::Group(g) => {
                for t in &g.items {
                    tree(t, from_llm, out);
                }
            }
            _ => {}
        }
    }
    let mut out = HashSet::new();
    for item in &f.items {
        if let syn::Item::Use(u) = item {
            tree(&u.tree, false, &mut out);
        }
    }
    out
}

struct Guard<'a> {
    rel: &'a str,
    dir: &'a str,
    llm_names: HashSet<String>,
    problems: &'a mut Vec<String>,
}

/// Whether attrs hold `#[cfg(test)]`: test code is not production code.
fn is_test(attrs: &[syn::Attribute]) -> bool {
    attrs.iter().any(|a| {
        a.path().is_ident("cfg")
            && match &a.meta {
                syn::Meta::List(l) => l.tokens.to_string().trim() == "test",
                _ => false,
            }
    })
}

fn last_ident(p: &syn::Path) -> String {
    p.segments
        .last()
        .map(|s| s.ident.to_string())
        .unwrap_or_default()
}

impl Guard<'_> {
    fn is_llm_trait(&self, p: &syn::Path) -> bool {
        let last = last_ident(p);
        if p.segments.len() == 1 {
            return self.llm_names.contains(&last);
        }
        (last == "Client" || last == "Embedder")
            && p.segments
                .iter()
                .any(|s| s.ident == "llm" || s.ident == "goap_llm")
    }
}

impl<'ast> Visit<'ast> for Guard<'_> {
    fn visit_item_mod(&mut self, m: &'ast syn::ItemMod) {
        if !is_test(&m.attrs) {
            syn::visit::visit_item_mod(self, m);
        }
    }

    fn visit_item_fn(&mut self, f: &'ast syn::ItemFn) {
        if !is_test(&f.attrs) {
            syn::visit::visit_item_fn(self, f);
        }
    }

    fn visit_item_impl(&mut self, i: &'ast syn::ItemImpl) {
        if is_test(&i.attrs) {
            return;
        }
        if let Some((_, tr, _)) = &i.trait_
            && self.is_llm_trait(tr)
        {
            let ty = match &*i.self_ty {
                syn::Type::Path(p) => last_ident(&p.path),
                _ => String::new(),
            };
            if !IMPLEMENTATIONS.contains(&(self.dir, ty.as_str())) {
                self.problems.push(format!(
                    "{}: {}.{ty} implements the model contract ({}) outside the ledger",
                    self.rel,
                    self.dir,
                    last_ident(tr)
                ));
            }
        }
        syn::visit::visit_item_impl(self, i);
    }

    fn visit_expr_call(&mut self, c: &'ast syn::ExprCall) {
        // llm::ClientFunc(...) / ClientFunc::shared(...) must wrap the gateway's complete
        if let syn::Expr::Path(p) = &*c.func {
            let segs: Vec<String> = p
                .path
                .segments
                .iter()
                .map(|s| s.ident.to_string())
                .collect();
            if segs.iter().any(|s| s == "ClientFunc") {
                let args: String = c
                    .args
                    .iter()
                    .map(quote_tokens)
                    .collect::<Vec<_>>()
                    .join(",");
                if c.args.len() != 1 || !(args.contains("gw") && args.contains("complete")) {
                    self.problems.push(format!(
                        "{}: llm::ClientFunc that is not the gateway's complete",
                        self.rel
                    ));
                }
            }
        }
        syn::visit::visit_expr_call(self, c);
    }

    fn visit_expr_method_call(&mut self, m: &'ast syn::ExprMethodCall) {
        // the router is below the ledger: only the service calls it
        let method = m.method.to_string();
        if (method == "complete" || method == "embed")
            && let syn::Expr::Field(f) = &*m.receiver
            && matches!(&f.member, syn::Member::Named(n) if n == "router")
            && self.rel != "platform/modelgw/src/service.rs"
        {
            self.problems.push(format!(
                "{}: router.{method} called outside Service",
                self.rel
            ));
        }
        syn::visit::visit_expr_method_call(self, m);
    }
}

/// The identifiers of an expression, as text (syn keeps no source text).
fn quote_tokens(e: &syn::Expr) -> String {
    struct Idents(Vec<String>);
    impl<'ast> Visit<'ast> for Idents {
        fn visit_ident(&mut self, i: &'ast syn::Ident) {
            self.0.push(i.to_string());
        }
    }
    let mut v = Idents(Vec::new());
    v.visit_expr(e);
    v.0.join(" ")
}
