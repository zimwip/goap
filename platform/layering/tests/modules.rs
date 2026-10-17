//! Port of `modules_test.go` and `architecture_test.go`: the modules of the repository, the dependencies of their
//! crates, and the architecture overview that draws them.

use std::collections::BTreeSet;

use goap_layering::{MODULES, Workspace, component_of, depends_on, root};
use regex::Regex;

/// Every crate, its tests included, depends only on the modules its own may depend on (ADR 0125 §1). Cargo lets a
/// crate depend on any crate of the workspace, so the rule is checked here.
#[test]
fn components() {
    let ws = Workspace::load().unwrap();
    let mut bad = Vec::new();
    for c in ws.crates.values() {
        let from = component_of(&c.pkg);
        if from.is_empty() {
            bad.push(format!("{} is in no module of the workspace", c.pkg));
            continue;
        }
        for d in &c.deps {
            let to = component_of(&d.pkg);
            if to != from && !depends_on(from).contains(&to) {
                let test = if d.kind == goap_layering::DepKind::Dev {
                    "the tests of "
                } else {
                    ""
                };
                bad.push(format!(
                    "{from} -> {to}: {test}{} depends on {}",
                    c.pkg, d.pkg
                ));
            }
        }
    }
    bad.sort();
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}

/// Each module depends on the modules it may depend on, and only those (Go: each go.mod requires them; here the
/// crates of a module together), and every crate names a crate of the workspace through `[workspace.dependencies]`
/// (`{ workspace = true }`, never a `path` of its own: docs/rust-port.md, "Layout").
#[test]
fn module_requirements() {
    let ws = Workspace::load().unwrap();
    let mut bad = Vec::new();
    for m in MODULES {
        let got: BTreeSet<&str> = ws
            .crates
            .values()
            .filter(|c| component_of(&c.pkg) == *m)
            .flat_map(|c| c.deps.iter().map(|d| component_of(&d.pkg)))
            .filter(|to| to != m)
            .collect();
        let want: BTreeSet<&str> = depends_on(m).iter().copied().collect();
        if got != want {
            bad.push(format!(
                "the crates of {m} depend on {got:?}, its module may depend on {want:?}"
            ));
        }
    }
    let names: BTreeSet<&str> = ws.crates.values().map(|c| c.name.as_str()).collect();
    for c in ws.crates.values() {
        let manifest = std::fs::read_to_string(c.dir.join("Cargo.toml")).unwrap();
        let mut section = String::new();
        for line in manifest.lines() {
            let line = line.trim();
            if line.starts_with('[') {
                section = line.to_string();
                continue;
            }
            if !section.contains("dependencies") {
                continue;
            }
            let Some((name, spec)) = line.split_once('=') else {
                continue;
            };
            let name = name.trim();
            if names.contains(name) && (!spec.contains("workspace = true") || spec.contains("path"))
            {
                bad.push(format!(
                    "{}/Cargo.toml names {name} without {{ workspace = true }}",
                    c.pkg
                ));
            }
        }
    }
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}

/// The architecture overview (docs/architecture-overview.html) names every module of the workspace, every binary of
/// platform/cmd and every service of the compose stack, so that a new one cannot be added without drawing it there
/// (CLAUDE.md, "Architecture overview"). It checks the names only: the flows are kept up to date by hand.
#[test]
fn architecture_overview() {
    let root = root();
    let html = std::fs::read_to_string(root.join("docs/architecture-overview.html")).unwrap();
    let mut bad = Vec::new();
    let mut missing = |attr: &str, name: &str| {
        if !html.contains(&format!("{attr}=\"{name}\"")) {
            bad.push(format!(
                "docs/architecture-overview.html does not show {} {name:?} ({attr})",
                attr.trim_start_matches("data-")
            ));
        }
    };
    let top: BTreeSet<String> = std::fs::read_dir(&root)
        .unwrap()
        .filter_map(|e| e.ok())
        .filter(|e| e.path().join("Cargo.toml").exists() || e.path().join("go.mod").exists())
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .collect();
    for m in MODULES {
        assert!(top.contains(*m), "module {m} has no directory at the root");
        missing("data-module", m);
    }
    let ws = Workspace::load().unwrap();
    let mut bins = 0;
    for c in ws
        .crates
        .values()
        .filter(|c| c.pkg.starts_with("platform/cmd/"))
    {
        for b in &c.bins {
            bins += 1;
            missing("data-binary", b);
        }
    }
    assert!(bins > 0, "no binary found under platform/cmd");
    let compose = std::fs::read_to_string(root.join("deploy/compose/docker-compose.yml")).unwrap();
    // the services built from this repository: `build: {..., args: {SERVICE: <binary>}}` under `  <service>:`
    let service = Regex::new(
        r"(?m)^  ([a-z][a-z0-9-]*):\n(?:    .*\n)*?    build: \{[^}]*SERVICE: [a-z0-9-]+",
    )
    .unwrap();
    let found: Vec<String> = service
        .captures_iter(&compose)
        .map(|c| c[1].to_string())
        .collect();
    assert!(
        !found.is_empty(),
        "no service built from the repository found in deploy/compose/docker-compose.yml"
    );
    for f in &found {
        missing("data-service", f);
    }
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}
