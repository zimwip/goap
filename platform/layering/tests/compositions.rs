use goap_layering::{root, sources};

/// Both compositions boot through graphsvc::boot: neither spells the platform seeds by hand.
#[test]
fn compositions_use_boot() {
    let mut bad = Vec::new();
    for dir in ["platform/cmd/goap-dev", "platform/cmd/graph"] {
        // a composition may spread its wiring over several files of its crate (cmd/goap-dev, ADR 0073)
        let files = sources(&root().join(dir)).unwrap();
        assert!(!files.is_empty(), "{dir}: no source");
        let src: String = files.iter().map(|s| s.code.as_str()).collect();
        if !src.contains("graphsvc::boot(") {
            bad.push(format!("{dir} does not call graphsvc::boot"));
        }
        for seed in [
            "seed_access",
            "seed_builtins",
            "seed_models",
            "seed_protected_aliases",
            "seed_behaviors",
            "seed_defaults",
            "seed_demo",
        ] {
            if src.contains(&format!("graphsvc::{seed}")) {
                bad.push(format!("{dir} calls graphsvc::{seed} directly"));
            }
        }
    }
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}
