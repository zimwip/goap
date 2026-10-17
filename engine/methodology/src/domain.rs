//! The domain types a methodology references and the loading of a methodology file (`domain.go`).

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::sync::{Arc, LazyLock};

use goap_def::TypeSet;
use goap_domain::parse_type_ref;
use regex::Regex;
use serde_json::Value;

use crate::methodology::{Error, KIND_BUILTIN, Methodology};
use crate::util::q;

/// BuiltinSet tells which builtin names exist (ADR 0062): the static list of goap-builtins, or the registry of an
/// engine.
pub trait BuiltinSet: Send + Sync {
    fn has_builtin(&self, name: &str) -> bool;
}

impl BuiltinSet for goap_builtins::Known {
    fn has_builtin(&self, name: &str) -> bool {
        goap_builtins::Known::has_builtin(self, name)
    }
}

impl Methodology {
    /// Resolve returns the methodology with the types in force, so that Validate and Compile check its references.
    pub fn resolve(&self, types: Arc<dyn TypeSet>) -> Methodology {
        let mut out = self.clone();
        out.types = Some(types);
        out
    }

    /// WithBuiltins returns the methodology with the builtins in force, so that Validate and Compile refuse an action
    /// naming an unknown one.
    pub fn with_builtins(&self, b: Arc<dyn BuiltinSet>) -> Methodology {
        let mut out = self.clone();
        out.builtins = Some(b);
        out
    }

    /// checkTypeRef checks a reference to a node type (link false) or a link type against the resolved types. It
    /// returns the problem, or "" (nothing is checked while the types are not resolved).
    pub(crate) fn check_type_ref(&self, r#ref: &str, link: bool) -> String {
        let Some(types) = &self.types else {
            return String::new();
        };
        let r = match parse_type_ref(r#ref) {
            Ok(r) => r,
            Err(e) => return e.to_string(),
        };
        if !r.qualified() {
            return format!(
                "type reference {} must be qualified: <namespace>@{}",
                q(r#ref),
                r#ref
            );
        }
        if link && !types.has_link_type(r#ref) {
            return format!("unknown link type {}", r#ref);
        }
        if !link && !types.has_node_type(r#ref) {
            return format!("unknown node type {}", r#ref);
        }
        String::new()
    }

    /// lintTypeRefs checks the type references a methodology uses in CEL literals and builtin params (ADR 0012).
    pub(crate) fn lint_type_refs(&self, add: &mut dyn FnMut(String, String)) {
        let scan = |add: &mut dyn FnMut(String, String), path: &str, expr: &str| {
            for re in NODE_TYPE_LITERALS.iter() {
                for g in re.captures_iter(expr) {
                    let msg = self.check_type_ref(&g[1], false);
                    if !msg.is_empty() {
                        add(path.to_string(), msg);
                    }
                }
            }
            for re in LINK_TYPE_LITERALS.iter() {
                for g in re.captures_iter(expr) {
                    let msg = self.check_type_ref(&g[1], true);
                    if !msg.is_empty() {
                        add(path.to_string(), msg);
                    }
                }
            }
        };
        for (i, c) in self.conditions.iter().enumerate() {
            scan(add, &format!("conditions[{i}].expr"), &c.expr);
        }
        for (i, me) in self.methods.iter().enumerate() {
            scan(add, &format!("methods[{i}].when"), &me.when);
        }
        for (i, a) in self.actions.iter().enumerate() {
            let path = format!("actions[{i}]");
            scan(add, &format!("{path}.when"), &a.when);
            scan(add, &format!("{path}.utility"), &a.utility);
            if let Some(e) = &a.expects {
                scan(add, &format!("{path}.expects.where"), &e.r#where);
            }
            if a.kind == KIND_BUILTIN
                && a.builtin == goap_builtins::GRAPH_PROPAGATE
                && let Some(Value::Array(lts)) = a.params.get("linkTypes")
            {
                for lt in lts {
                    if let Value::String(s) = lt {
                        let msg = self.check_type_ref(s, true);
                        if !msg.is_empty() {
                            add(format!("{path}.params.linkTypes"), msg);
                        }
                    }
                }
            }
        }
    }

    /// Namespaces lists the namespaces a methodology uses: its target namespace and the namespaces of the qualified
    /// type references it makes (CEL literals, expectations, builtin link types).
    pub fn namespaces(&self) -> Vec<String> {
        let mut set: BTreeSet<String> = BTreeSet::new();
        if !self.namespace.is_empty() {
            set.insert(self.namespace.clone());
        }
        fn r#ref(set: &mut BTreeSet<String>, s: &str) {
            if let Ok(r) = parse_type_ref(s)
                && r.qualified()
            {
                set.insert(r.namespace);
            }
        }
        let scan = |set: &mut BTreeSet<String>, expr: &str| {
            for re in NODE_TYPE_LITERALS.iter().chain(LINK_TYPE_LITERALS.iter()) {
                for g in re.captures_iter(expr) {
                    r#ref(set, &g[1]);
                }
            }
        };
        for c in &self.conditions {
            scan(&mut set, &c.expr);
        }
        for a in &self.actions {
            scan(&mut set, &a.when);
            scan(&mut set, &a.utility);
            if let Some(e) = &a.expects {
                scan(&mut set, &e.r#where);
                r#ref(&mut set, &e.produce.node_type);
                if let Some(l) = &e.link {
                    r#ref(&mut set, &l.r#type);
                }
            }
            if let Some(Value::Array(lts)) = a.params.get("linkTypes") {
                for lt in lts {
                    if let Value::String(s) = lt {
                        r#ref(&mut set, s);
                    }
                }
            }
        }
        set.into_iter().collect()
    }
}

/// Patterns of domain types used inside CEL expressions: `"T" in n.types` and `n.type == "T"` on change nodes,
/// `l.type == "L"` on links.
static NODE_TYPE_LITERALS: LazyLock<Vec<Regex>> = LazyLock::new(|| {
    vec![
        Regex::new(r#""([^"]+)"\s+in\s+[\w.]+\.types\b"#).expect("regexp"),
        Regex::new(r#"\b[nxrtfc]\.type\s*==\s*"([^"]+)""#).expect("regexp"),
    ]
});
static LINK_TYPE_LITERALS: LazyLock<Vec<Regex>> =
    LazyLock::new(|| vec![Regex::new(r#"\bl\.type\s*==\s*"([^"]+)""#).expect("regexp")]);

/// domainsDirFor finds the "domains" directory beside the methodologies directory of a file.
fn domains_dir_for(path: &Path) -> PathBuf {
    let base = path.parent().unwrap_or(Path::new(".")).to_path_buf();
    let mut dir = base.clone();
    for _ in 0..3 {
        dir = dir.join("..");
        let d = dir.join("domains");
        if d.is_dir() {
            return d;
        }
    }
    base.join("..").join("domains")
}

/// LoadFile parses a methodology file and resolves its types against the domains of the "domains" directory next to
/// the methodology's directory (the layout of the repository: methodologies/ and domains/).
pub fn load_file(path: impl AsRef<Path>) -> Result<Methodology, Error> {
    let path = path.as_ref();
    let src = std::fs::read(path)?;
    let m = Methodology::parse(&src)?;
    let ds = goap_def::load_domains(domains_dir_for(path))?;
    Ok(m.resolve(Arc::new(goap_def::domain_types(&ds))))
}
