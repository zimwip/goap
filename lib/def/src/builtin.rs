//! The domains shipped with the platform (ADR 0012 §4).

use std::sync::LazyLock;

use crate::domain::{Domain, parse_domain};

static BUILTIN: LazyLock<Vec<Domain>> = LazyLock::new(|| {
    ["methodology.yaml", "organisation.yaml", "platform.yaml"]
        .into_iter()
        .map(|f| {
            let src = goap_domains_builtin::file(f).unwrap_or_else(|| panic!("{f}: not embedded"));
            // embedded, covered by the tests
            parse_domain(src.as_bytes()).unwrap_or_else(|e| panic!("{f}: {e}"))
        })
        .collect()
});

/// BuiltinDomains returns the domains that ship with the platform (ADR 0012 §4), known before any domain is loaded:
/// the meta-domain "methodology", which types the definition nodes of the methodologies (ADR 0023), and "organisation"
/// and "platform", whose types the platform itself reads (units, adapters, users, policies; MCPs, adapter definitions,
/// model configuration). They are frozen: they change with the code (their YAML), the registry never versions them.
pub fn builtin_domains() -> &'static [Domain] {
    &BUILTIN
}

/// IsBuiltinDomain reports the name (namespace) of a built-in domain.
pub fn is_builtin_domain(name: &str) -> bool {
    builtin_domains().iter().any(|d| d.name == name)
}
